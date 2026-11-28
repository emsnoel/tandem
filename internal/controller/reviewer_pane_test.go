package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// The reviewer pane: a third agent, on its own model, that takes every review
// prompt. The builder never reviews its own work and the planner never reviews
// its own plan.

const (
	reviewerPane = "reviewer-pane"
	reviewerConv = "reviewer-conversation"
)

// reviewerStart is a run's start request with a reviewer pane. Options set the
// collaboration and review handback.
func (e *cenv) reviewerStart() StartRequest {
	return e.startRequest(func(r *StartRequest) { r.ReviewerSession = reviewerPane })
}

func (e *cenv) startWith(req StartRequest) {
	e.t.Helper()
	id, err := e.c.Start(ctx, req)
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
}

// startReviewerHandback starts a mailbox run, pinned to the production
// acceptance policy, with a reviewer pane.
func (e *cenv) startReviewerHandback() {
	e.t.Helper()
	policy, err := e.c.put(ctx, `{"review":"handback"}`)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy}); err != nil {
		e.t.Fatal(err)
	}
	e.registerPlanner(testCollab)
	req := e.reviewerStart()
	req.Collaboration, req.ReviewProtocol, req.ReviewDelivery, req.AcceptancePolicyPath = testCollab, 3, "mailbox", e.policyPath()
	e.startWith(req)
}

func (e *cenv) reviewPromptOf(att workflow.Attempt) workflow.Effect {
	e.t.Helper()
	for _, p := range e.effects(effectReviewPrompt) {
		if p.AttemptID == att.ID {
			return p
		}
	}
	e.t.Fatalf("attempt %s has no review prompt", att.ID)
	return workflow.Effect{}
}

func (e *cenv) deliveryOf(effectID string) runner.Delivery {
	e.t.Helper()
	e.tr.mu.Lock()
	defer e.tr.mu.Unlock()
	for _, d := range e.tr.sends {
		if d.EffectID == effectID {
			return d
		}
	}
	e.t.Fatalf("effect %s was never sent", effectID)
	return runner.Delivery{}
}

// reviewEnvelope writes a reviewer's payload and returns the version 3
// envelope for it, for a test that needs Ingest's own answer.
func (e *cenv) reviewEnvelope(att workflow.Attempt, prompt workflow.Effect, kind, payload string) []byte {
	e.t.Helper()
	e.sendN++
	name := fmt.Sprintf("payloads/%s-%d.json", kind, e.sendN)
	if err := os.WriteFile(filepath.Join(RunDir(e.relay, e.runID), name), []byte(payload), 0o644); err != nil {
		e.t.Fatal(err)
	}
	raw, _ := json.Marshal(Envelope{SchemaVersion: 3, EventID: fmt.Sprintf("%032x", 0xD000+e.sendN), RunID: e.runID, TaskID: att.TaskID,
		AttemptID: att.ID, JobID: prompt.JobID, EffectID: prompt.ID, Nonce: prompt.Nonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)})
	return raw
}

// builderPaneLooksDone makes the builder's pane, which is what observing a
// review prompt by its effect reaches, report a finished turn in the builder's
// own conversation. Only the reviewer pane may speak for a review.
func (e *cenv) builderPaneLooksDone(att workflow.Attempt) {
	e.tr.set(e.reviewPromptOf(att).ID, runner.Observation{State: runner.ObsCompleted, ExternalID: "builder-conversation"})
}

func TestReviewerPaneTakesTheReviewPrompt(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.startWith(e.reviewerStart())
	att := e.toReview("task-1", 1)

	prompt := e.reviewPromptOf(att)
	if d := e.deliveryOf(prompt.ID); d.SessionID != reviewerPane || d.AgentKind != "claude" {
		t.Fatalf("the review prompt went to %s (%s), not the reviewer pane", d.SessionID, d.AgentKind)
	}
	if d := e.deliveryOf(e.builderEffect(att).ID); d.SessionID != "builder-session" || d.AgentKind != "codex" {
		t.Fatalf("the task went to %s (%s), not the builder", d.SessionID, d.AgentKind)
	}

	// The builder's pane looks finished; the reviewer is still working. The
	// result is not taken.
	e.builderPaneLooksDone(att)
	e.tr.setAgent(reviewerPane, runner.Observation{State: runner.ObsRunning, ExternalID: reviewerConv})
	if _, err := e.c.RecordReview(ctx, e.runID, e.resultFor(att, workflow.ReviewStatusPassed)); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("a result was taken while the reviewer pane was still working: %v", err)
	}
	e.tr.setAgent(reviewerPane, runner.Observation{State: runner.ObsCompleted, ExternalID: reviewerConv})
	if _, err := e.c.RecordReview(ctx, e.runID, e.resultFor(att, workflow.ReviewStatusPassed)); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.accepted() != 1 {
		t.Fatalf("a passing review from the reviewer pane accepted %d tasks", e.accepted())
	}
}

func TestManualReviewerGetsPersistedRequirements(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.plan = filepath.Join(t.TempDir(), "plan.md")
	const constraints = "Keep the CLI."
	const first = "### Task 1: Sort\n\nSort ascending."
	const second = "### Task 2: Sort\n\nSort descending."
	if err := os.WriteFile(e.plan, []byte(constraints+"\n\n"+first+"\n\n"+second+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e.startWith(e.reviewerStart())
	check := func(a workflow.Attempt, want, other string) {
		t.Helper()
		p := e.reviewPromptOf(a)
		if d := e.deliveryOf(p.ID); d.SessionID != reviewerPane || d.PromptHash != p.PayloadHash {
			t.Fatalf("review delivery = %+v, want persisted prompt in reviewer pane", d)
		}
		text := e.promptText(effectReviewPrompt, a)
		if !strings.Contains(text, want) || !strings.Contains(text, constraints) || strings.Contains(text, other) || strings.Contains(text, "Edited plan") {
			t.Errorf("attempt %s should review %q under %q:\n%s", a.ID, want, constraints, text)
		}
		data := e.manualReviewData(a)
		baseline := reviewPrompt(data)
		if len(text) > len(baseline) {
			t.Errorf("review grew from %d to %d bytes", len(baseline), len(text))
		}
		const reply = "Reply with ONE JSON object"
		_, gotEvidence, gotReply := strings.Cut(text, reply)
		_, oldEvidence, _ := strings.Cut(baseline, reply)
		if !gotReply || gotEvidence != oldEvidence {
			t.Error("review changed the result instructions or patch evidence")
		}
		t.Logf("task %d, attempt %d, empty patch %t: %d → %d bytes, one delivery, zero reviewer reads",
			data.Task, a.Ordinal, data.Patch == "", len(baseline), len(text))
	}
	finish := func(a workflow.Attempt, status string, findings ...workflow.Finding) {
		t.Helper()
		obs := runner.Observation{State: runner.ObsCompleted, ExternalID: reviewerConv}
		e.tr.set(e.reviewPromptOf(a).ID, obs)
		e.tr.setAgent(reviewerPane, obs)
		if _, err := e.c.RecordReview(ctx, e.runID, e.resultFor(a, status, findings...)); err != nil {
			t.Fatal(err)
		}
		e.tick()
	}
	a := e.toReview("task-1", 1)
	check(a, first, second)
	if err := os.WriteFile(e.plan, []byte("Edited plan\n\n### Task 1: Other\n\n### Task 2: Other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	finish(a, workflow.ReviewStatusChangesRequested, workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "Fixture requests a repair"})
	repair := e.toReviewUnchanged("task-1")
	if repair.ParentAttemptID != a.ID {
		t.Fatalf("attempt %s should repair %s", repair.ID, a.ID)
	}
	check(repair, first, second)
	finish(repair, workflow.ReviewStatusPassed)
	b := e.toReview("task-2", 2)
	check(b, second, first)
	if n := e.delivered()[effectReviewPrompt]; n != 3 {
		t.Fatalf("review deliveries = %d, want one per attempt", n)
	}
}

func (e *cenv) manualReviewData(a workflow.Attempt) reviewPromptData {
	e.t.Helper()
	result := e.resultFor(a, workflow.ReviewStatusPassed)
	current, err := e.st.GetAttempt(ctx, a.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	patch, err := e.c.patchText(ctx, current)
	if err != nil {
		e.t.Fatal(err)
	}
	task, err := e.c.taskOf(ctx, e.runRow(), current)
	if err != nil {
		e.t.Fatal(err)
	}
	return reviewPromptData{Task: task.Ordinal, Slug: task.ID, Job: result.JobID, Attempt: a.ID,
		PatchHash: result.PatchHash, Tree: result.TreeHash, Patch: patch, WarmPane: true}
}

func TestManualReviewerLongRequirementBudget(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.plan = filepath.Join(t.TempDir(), "plan.md")
	const constraints = "Keep the CLI."
	brief := "### Task 1: Sort\n\n" + strings.Repeat("Sort ascending and preserve the order of equal values.\n", 20) + "Handle the empty input."
	if err := os.WriteFile(e.plan, []byte(constraints+"\n\n"+brief+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e.startWith(e.reviewerStart())
	if err := os.WriteFile(e.plan, []byte("Changed constraints.\n\n### Task 1: Changed brief\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := e.toReview("task-1", 1)
	data := e.manualReviewData(a)
	baseline := reviewPrompt(data)
	if got := e.promptText(effectReviewPrompt, a); got != baseline {
		t.Fatal("the default long review prompt grew or changed")
	}
	if n := e.delivered()[effectReviewPrompt]; n != 1 {
		t.Fatalf("review deliveries = %d, want one", n)
	}
	spec, err := e.c.reviewTaskSpec(ctx, e.tasks()[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(RunDir(e.relay, e.runID), "review", a.ID, "requirements.txt")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("default review should skip publishing the over-budget reference: %v", err)
	}
	candidate := proposeManualReview(data, spec, path, len(baseline))
	if candidate.readCommand == "" || !strings.Contains(candidate.prompt, candidate.readCommand) || !strings.Contains(candidate.readCommand, a.ID) {
		t.Fatalf("long requirement proposal lacks an attempt-bound read command:\n%s", candidate.prompt)
	}
	// Exercise the exact command the pane would get, against its checkout
	// relay. The command works even if the reviewer has changed directories.
	if err := publishReviewRequirement(path, candidate.readOutput); err != nil {
		t.Fatal(err)
	}
	read := exec.Command("sh", "-c", candidate.readCommand)
	read.Dir = t.TempDir()
	out, err := read.CombinedOutput()
	want := "Brief:\n" + brief + "\n\nConstraints:\n" + constraints + "\n"
	if err != nil || string(out) != want {
		t.Fatalf("read requirements: %v\n%s\nwant:\n%s", err, out, want)
	}
	if cost := len(candidate.prompt) + len(candidate.readCommand) + len(out); cost != candidate.bytes() || cost <= len(baseline) {
		t.Fatalf("long candidate costs %d bytes including retrieval, budget %d", cost, len(baseline))
	}
	t.Logf("long: baseline/default %d bytes, one delivery, no read. Candidate %d prompt bytes + %d call bytes + %d output bytes = %d bytes minimum, one delivery and one extra model continuation",
		len(baseline), len(candidate.prompt), len(candidate.readCommand), len(out), candidate.bytes())
}

// Every other prompt keeps its agent: a task goes to the builder, a notice to
// the planner.
func TestReviewerPaneChangesNoOtherTarget(t *testing.T) {
	t.Parallel()
	cfg := runConfig{BuilderKind: "codex", BuilderSession: "b", PlannerKind: "planner", PlannerSession: "p", ReviewerKind: "claude", ReviewerSession: "r"}
	for kind, want := range map[string]string{workflow.EffectBuilderDispatch: "b", workflow.EffectCancel: "b", effectNotify: "p",
		effectVerifyPrompt: "p", effectRecoveryDecision: "p", effectReviewPrompt: "r"} {
		if _, got := cfg.agentFor(workflow.Effect{Kind: kind}); got != want {
			t.Errorf("%s goes to %s, want %s", kind, got, want)
		}
	}
}

// No run reviews in the builder's pane: a review prompt goes to the reviewer's
// kind and session, whatever the builder is.
func TestReviewPromptNeverGoesToTheBuilder(t *testing.T) {
	t.Parallel()
	cfg := runConfig{BuilderKind: "codex", BuilderSession: "b", PlannerKind: "planner", PlannerSession: "p", ReviewerKind: "claude", ReviewerSession: "r"}
	if kind, session := cfg.agentFor(workflow.Effect{Kind: effectReviewPrompt}); kind != "claude" || session != "r" {
		t.Errorf("the review prompt goes to %s (%s), want the reviewer claude (r)", session, kind)
	}
}

func TestStartRefusesWithoutAReviewerPane(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	req := e.reviewerStart()
	req.ReviewerKind, req.ReviewerSession = "", ""
	_, err := e.c.Start(ctx, req)
	if !errors.Is(err, workflow.ErrInvalidTransition) || !strings.Contains(err.Error(), "a run needs a reviewer pane: the builder never reviews") || !strings.Contains(err.Error(), "tandem-pair --reviewer KIND") {
		t.Fatalf("Start: %v", err)
	}
	if n := e.countRuns(); n != 0 {
		t.Fatalf("a refused start created %d runs", n)
	}
	if _, statErr := os.Stat(filepath.Join(e.relay, ActiveRunFile)); !os.IsNotExist(statErr) {
		t.Fatalf("a refused start left the active-run marker: %v", statErr)
	}
}

func TestStartRefusesAReviewerThatIsTheBuilderOrThePlanner(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*StartRequest){
		"the builder's pane":                func(r *StartRequest) { r.ReviewerSession = r.BuilderSession },
		"the planner's pane":                func(r *StartRequest) { r.ReviewerSession = r.PlannerSession },
		"a pane with no kind":               func(r *StartRequest) { r.ReviewerKind = "" },
		"a kind with no pane":               func(r *StartRequest) { r.ReviewerSession = "" },
		"a session name a pane cannot have": func(r *StartRequest) { r.ReviewerSession = "two words" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			req := e.reviewerStart()
			edit(&req)
			if _, err := e.c.Start(ctx, req); !errors.Is(err, workflow.ErrInvalidTransition) {
				t.Fatalf("Start: %v", err)
			}
			if n := e.countRuns(); n != 0 {
				t.Fatalf("a refused start created %d runs", n)
			}
		})
	}
}

// The configuration of a run records its reviewer pane and pins the builder
// handback relay.
func TestRunConfigRecordsTheReviewerPane(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.start()
	run, err := e.st.GetRun(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := e.art.ReadAll(ctx, run.ConfigHash, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := e.c.config(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"root":"` + cfg.Root + `","plan_path":"` + e.plan + `","builder_kind":"codex","builder_session":"builder-session",` +
		`"planner_kind":"planner","planner_session":"planner-session","check_command":"true","task_input_limit":0,"run_input_limit":0,` +
		`"reviewer_kind":"claude","reviewer_session":"reviewer-session","builder_handback_relay":".tandem-relay"}` + "\n"
	if string(raw) != want {
		t.Fatalf("the configuration of a run changed:\n got %s\nwant %s", raw, want)
	}

	r := newCEnv(t)
	r.startWith(r.reviewerStart())
	if rc, _, err := r.c.load(ctx, r.runID); err != nil {
		t.Fatal(err)
	} else if rcfg, _ := r.c.config(ctx, rc); rcfg.ReviewerKind != "claude" || rcfg.ReviewerSession != reviewerPane {
		t.Fatalf("the reviewer was not recorded: %+v", rcfg)
	}
	ag, err := RunAgents(ctx, r.st, r.art, r.runID)
	if err != nil || ag.ReviewerKind != "claude" || ag.ReviewerSession != reviewerPane || ag.BuilderSession != "builder-session" {
		t.Fatalf("RunAgents %+v %v", ag, err)
	}
}

// With a reviewer pane, its passing review is the independent check: the
// planner is not asked to verify, and the task is accepted.
func TestReviewerPassAcceptsWithoutPlannerVerification(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.startReviewerHandback()
	att := e.toReview("task-1", 1)
	prompt := e.reviewPromptOf(att)
	// The builder's pane is in another conversation. A start naming the
	// reviewer's conversation agrees with what the reviewer pane shows.
	e.builderPaneLooksDone(att)
	e.tr.setAgent(reviewerPane, runner.Observation{State: runner.ObsCompleted, ExternalID: reviewerConv})
	e.sendReview(att, prompt, "review_started", `{"session_id":"`+reviewerConv+`"}`+"\n")
	e.sendReview(att, prompt, "review_result", string(encode(e.resultFor(att, workflow.ReviewStatusPassed))))
	for i := 0; i < 6; i++ {
		e.tick()
	}
	if e.accepted() != 1 || e.acceptEvents() != 1 {
		t.Fatalf("accepted %d (events %d); run %v", e.accepted(), e.acceptEvents(), e.pause())
	}
	if n := len(e.effects(effectVerifyPrompt)); n != 0 || e.delivered()[effectVerifyPrompt] != 0 {
		t.Fatalf("the planner was asked to verify (%d prompts) although the reviewer pane reviewed", n)
	}
	if turns, err := e.c.plannerTurns(ctx, e.runID); err != nil || turns != (PlannerTurns{}) {
		t.Fatalf("planner turns %+v %v", turns, err)
	}
}

// A start whose session disagrees with the reviewer pane is refused: the
// mailbox observes the reviewer pane, not the builder's.
func TestReviewerStartIsCheckedAgainstTheReviewerPane(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.startReviewerHandback()
	att := e.toReview("task-1", 1)
	prompt := e.reviewPromptOf(att)
	e.tr.set(prompt.ID, runner.Observation{State: runner.ObsCompleted, ExternalID: "builder-conversation"})
	e.tr.setAgent(reviewerPane, runner.Observation{State: runner.ObsRunning, ExternalID: reviewerConv})
	_, err := e.c.Ingest(ctx, e.reviewEnvelope(att, prompt, "review_started", `{"session_id":"builder-conversation"}`+"\n"))
	var rej *RejectError
	if !errors.As(err, &rej) || rej.Code != "session_mismatch" {
		t.Fatalf("a start naming the builder's conversation was taken: %v", err)
	}
}

// A reviewer that turns out to sit in the writer's conversation does not count.
func TestReviewerInTheWritersConversationDoesNotCount(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.startReviewerHandback()
	att := e.toReview("task-1", 1)
	writer := e.c.sessionOfEffect(ctx, e.builderEffect(att))
	if writer == "" {
		t.Fatal("the fixture's builder has no conversation")
	}
	prompt := e.reviewPromptOf(att)
	e.tr.setAgent(reviewerPane, runner.Observation{State: runner.ObsCompleted, ExternalID: writer})
	e.sendReview(att, prompt, "review_started", `{"session_id":"`+writer+`"}`+"\n")
	e.sendReview(att, prompt, "review_result", string(encode(e.resultFor(att, workflow.ReviewStatusPassed))))
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if st, code := e.runState(); st != "paused" || code != pauseVerificationIdentity || e.accepted() != 0 {
		t.Fatalf("run %s/%s, accepted %d: a self-review counted", st, code, e.accepted())
	}
}

// The reviewer pane's launches are sources of their own, under the reviewer's
// provider and conversation, so the review's tokens are reported apart from
// the builder's.
func TestReviewerTokensAreTheirOwnSource(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	logs := newFakeLogs(t)
	e.logs = logs
	e.c = e.newController()
	policy, err := e.c.put(ctx, `{"input_ceilings":{"codex":100000}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy}); err != nil {
		t.Fatal(err)
	}
	// A claude builder and a codex reviewer: only the reviewer writes a codex
	// log, so every codex token is the reviewer's.
	req := e.reviewerStart()
	req.BuilderKind, req.ReviewerKind, req.Collaboration = "claude", "codex", testCollab
	e.startWith(req)
	att := e.toReview("task-1", 1)
	prompt := e.reviewPromptOf(att)
	src, err := e.st.Source(ctx, sourceID(prompt.ID))
	if err != nil {
		t.Fatal(err)
	}
	conversation := "session-" + prompt.ID[:6]
	if src.Role != "reviewer" || src.Provider != "codex" || src.SessionID != conversation || src.JobID != prompt.JobID {
		t.Fatalf("the review's source is %+v", src)
	}
	logs.codexSession(t, conversation, codexTotal(700, 100, 40))
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	rep := e.snapshot(t)
	if got := rep.Snapshot.Totals["codex"].InputTotal; got == nil || *got != 700 {
		t.Fatalf("the reviewer's tokens are not counted: %+v", rep.Snapshot.Totals)
	}
	key := ""
	for _, s := range rep.Snapshot.ExpectedSources {
		if s.ID == src.ID && s.Role == "reviewer" && s.Provider == "codex" {
			key = s.SourceKey
		}
	}
	if key == "" || len(rep.Snapshot.Events) == 0 {
		t.Fatalf("the report has no reviewer source %s, or no events: %+v", src.ID, rep.Snapshot.ExpectedSources)
	}
	for _, ev := range rep.Snapshot.Events {
		if ev.SourceKey != key {
			t.Fatalf("a codex event is charged to %s, not the reviewer's source %s", ev.SourceKey, key)
		}
	}
}

// A warm pane keeps earlier tasks in its context. Its packet says to judge it
// on its own.
func TestReviewerPacketSaysToJudgeItAlone(t *testing.T) {
	t.Parallel()
	for _, protocol := range []int{2, 3} {
		e := newCEnv(t)
		if protocol == 3 {
			e.startReviewerHandback()
		} else {
			e.startWith(e.reviewerStart())
		}
		a := e.toReview("task-1", 1)
		if got := e.promptText(effectReviewPrompt, a); !strings.Contains(got, judgeAlone) && (protocol == 3 || !strings.Contains(got, judgeManualTask)) {
			t.Errorf("protocol %d: the reviewer pane's packet does not say to judge it alone:\n%s", protocol, got)
		}
	}
}

// A run an older tandem started pinned no reviewer pane. It pauses with a code
// that names the way on, and sends nothing, instead of addressing a review
// prompt to no agent.
func TestARunWithNoPinnedReviewerPausesAtTickAndRecover(t *testing.T) {
	t.Parallel()
	for _, drive := range []string{"tick", "recover"} {
		t.Run(drive, func(t *testing.T) {
			e := newCEnv(t)
			e.startWith(e.reviewerStart())
			run, err := e.st.GetRun(ctx, e.runID)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := e.c.config(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ReviewerKind, cfg.ReviewerSession = "", ""
			hash, err := e.c.putJSON(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.raw().Exec(`UPDATE runs SET config_hash=? WHERE id=?`, hash, e.runID); err != nil {
				t.Fatal(err)
			}
			if drive == "tick" {
				e.tick()
			} else if err := e.c.Recover(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			if st, code := e.runState(); st != "paused" || code != pauseNoReviewer {
				t.Fatalf("run %s/%s, want paused/%s", st, code, pauseNoReviewer)
			}
			if n := e.delivered()[workflow.EffectBuilderDispatch] + e.delivered()[effectReviewPrompt]; n != 0 {
				t.Fatalf("a run with no reviewer sent %d prompts", n)
			}
		})
	}
}

func (e *cenv) countRuns() int {
	e.t.Helper()
	n, err := e.st.CountRuns(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}
