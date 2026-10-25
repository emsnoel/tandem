package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

type packetManifest struct {
	TaskID     string `json:"task_id"`
	Limit      int    `json:"limit"`
	PromptHash string `json:"prompt_hash"`
	Estimated  struct {
		Total int `json:"total"`
	} `json:"estimated"`
	Selected []struct {
		ID       string `json:"id"`
		Required bool   `json:"required"`
	} `json:"selected"`
	Omitted []struct{ ID, Reason string }
}

// packet reads an attempt's persisted prompt and manifest.
func (e *cenv) packet(att workflow.Attempt) (string, packetManifest) {
	e.t.Helper()
	raw, err := e.art.ReadAll(ctx, att.PacketHash, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	mh, found, err := e.st.AttemptManifest(ctx, att.ID)
	if err != nil || !found {
		e.t.Fatalf("no manifest for attempt %s: %v %v", att.ID, found, err)
	}
	mraw, err := e.art.ReadAll(ctx, mh, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	var m packetManifest
	if err := json.Unmarshal(mraw, &m); err != nil {
		e.t.Fatal(err)
	}
	return string(raw), m
}

func TestBuilderPromptIsThePersistedPacketWithItsManifest(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.current("task-1")
	prompt, m := e.packet(att)
	eff := e.builderEffect(att)
	for _, want := range []string{"Implement task 1 of 2 (task-1)", "**Goal:** greet.", "Do the first thing.", "Your task:",
		"tandem-handback --task 1 --run " + e.runID + " --attempt " + att.ID, "--nonce " + eff.Nonce + " --started", "--report <file>"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Do the second thing.") {
		t.Error("another task's text is in this task's packet")
	}
	sum := sha256.Sum256([]byte(prompt))
	if m.TaskID != "task-1" || m.Limit != taskcontext.DefaultMaxEstimatedTokens || m.PromptHash != hex.EncodeToString(sum[:]) || m.Estimated.Total != len(prompt) {
		t.Errorf("manifest %+v for a %d byte prompt", m, len(prompt))
	}
	if eff.PayloadHash != att.PacketHash {
		t.Error("the builder is sent something other than the persisted packet")
	}
}

func TestContextTooLargePausesTheRunWithoutAnAttempt(t *testing.T) {
	e := newCEnv(t)
	run, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.BuilderSession, r.PlannerSession, r.ContextLimit, r.CheckCommand = "b", "p", 300, ""
	}))
	if !errors.Is(err, workflow.ErrContextTooLarge) || run == "" {
		t.Fatalf("Start: %q %v", run, err)
	}
	e.runID = run
	if st, code := e.runState(); st != "paused" || code != "context_too_large" {
		t.Fatalf("run is %s/%s", st, code)
	}
	if as, _ := e.st.TaskAttempts(ctx, run, "task-1"); len(as) != 0 {
		t.Errorf("%d attempts were created for a packet that was refused", len(as))
	}
	all, _ := e.st.RunEffects(ctx, run)
	if len(all) != 0 {
		t.Errorf("%d effects were created", len(all))
	}
	e.tick()
	if len(e.tr.sends) != 0 {
		t.Error("a prompt was sent for a refused packet")
	}
	if !strings.Contains(err.Error(), "constraints") && !strings.Contains(err.Error(), "brief") {
		t.Errorf("the refusal must name the sections: %v", err)
	}
}

func TestLaterPacketsCarryDecisionsAndLabelClaims(t *testing.T) {
	e := newCEnv(t)
	e.start()
	scope := []string{"internal/state"}
	if _, err := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindApprovedDecision, Text: "use sqlite everywhere", Scope: scope, Required: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.RecordClaim(ctx, e.runID, ClaimRequest{TaskID: "task-1", Text: "the builder says postgres is nicer", Scope: scope}); err != nil {
		t.Fatal(err)
	}
	e.accept("task-1", 1)
	prompt, m := e.packet(e.current("task-2"))
	i, j, k := strings.Index(prompt, "must honour"), strings.Index(prompt, "use sqlite everywhere"), strings.Index(prompt, "Your task:")
	if i < 0 || j < i || k < j {
		t.Errorf("a required decision comes before the task:\n%s", prompt)
	}
	if !strings.Contains(prompt, "builder claim, unverified") || !strings.Contains(prompt, "postgres is nicer") {
		t.Errorf("the claim is shown, labelled:\n%s", prompt)
	}
	if len(m.Selected) != 2 || !m.Selected[0].Required {
		t.Errorf("%+v", m.Selected)
	}
}

func TestRequiredConflictPausesTheRunNamingBothSources(t *testing.T) {
	e := newCEnv(t)
	e.start()
	scope := []string{"internal/state"}
	a, _ := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindApprovedDecision, Text: "use sqlite", Scope: scope, Required: true})
	b, _ := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindApprovedDecision, Text: "use postgres", Scope: scope, Required: true})

	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	err := e.c.Tick(ctx, e.runID)
	if !errors.Is(err, workflow.ErrMemoryConflict) || !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) {
		t.Fatalf("Tick: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != "memory_conflict" {
		t.Errorf("run is %s/%s", st, code)
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-2"); len(as) != 0 {
		t.Error("task 2 was started from a contradiction")
	}
}

func TestPacketSizeDoesNotGrowWithUnrelatedHistory(t *testing.T) {
	plan := "# P\n\n**Goal:** g.\n\n### Task 1: A\nTask-ID: a\n\nDo a.\n\n### Task 2: B\nTask-ID: b\n\nDo b.\n\n### Task 3: C\nTask-ID: c\nDepends-On: a\n\nDo c.\n"
	size := func(noise int) (int, string) {
		e := newCEnv(t)
		e.setPlan(plan)
		e.start()
		e.accept("a", 1)
		if as, _ := e.st.TaskAttempts(ctx, e.runID, "b"); len(as) == 0 {
			// b starts after a (plan order); finish it and record its noise
			e.tick()
		}
		att := e.current("b")
		for i := 0; i < noise; i++ {
			if _, err := e.c.RecordClaim(ctx, e.runID, ClaimRequest{TaskID: "b", Text: strings.Repeat("noise ", 30) + string(rune('a'+i%26)) + strings.Repeat("x", i), Scope: []string{"cmd/other"}}); err != nil {
				t.Fatal(err)
			}
		}
		e.mustSend("b", workflow.EventBuilderStarted, "{}\n")
		e.work(2)
		e.settle(att)
		e.mustSend("b", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
		e.tick()
		if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
			t.Fatal(err)
		}
		e.tick()
		_, m := e.packet(e.current("c"))
		return m.Estimated.Total, m.PromptHash
	}
	quiet, _ := size(0)
	loud, _ := size(60)
	if quiet != loud {
		t.Errorf("task c's packet is %d bytes after 0 unrelated claims and %d after 60", quiet, loud)
	}
}

func TestStaleRequiredFactPausesTheRunBeforeDispatch(t *testing.T) {
	e := newCEnv(t)
	e.start()
	// A required decision bound to content that is not what task-1 produced.
	val, _ := e.c.put(ctx, "use sqlite")
	scope, _ := e.c.putJSON(ctx, []string{"internal/state"})
	src, _ := e.c.put(ctx, "source")
	stale, _ := e.c.put(ctx, "a binding that does not match")
	entry := memory.Entry{ID: derive("stale-required"), RunID: e.runID, TaskID: "task-1", Kind: memory.KindApprovedDecision, Trust: memory.TrustApproved,
		ScopeHash: scope, SourceHash: src, ValidityHash: stale, ValueHash: val, Required: true, Dependencies: []string{}}
	if err := e.st.PutMemory(ctx, entry); err != nil {
		t.Fatal(err)
	}
	// With no dependencies the binding of [] is the one that is current, so this
	// entry's hash is not.
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	err := e.c.Tick(ctx, e.runID)
	if !errors.Is(err, workflow.ErrIncompleteEvidence) || !strings.Contains(err.Error(), entry.ID) {
		t.Fatalf("Tick: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != "context_incomplete" {
		t.Errorf("run is %s/%s", st, code)
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-2"); len(as) != 0 {
		t.Error("task 2 was dispatched without a required fact")
	}
}

// sendFor plays the builder of a specific attempt, which need not be the current one.
func (e *cenv) sendFor(att workflow.Attempt, taskID, kind, payload string) error {
	e.t.Helper()
	eff := e.builderEffect(att)
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "builder")
	e.sendN++
	name := fmt.Sprintf("payloads/%s-old-%d.json", kind, e.sendN)
	dir := RunDir(e.relay, e.runID)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
		e.t.Fatal(err)
	}
	env := Envelope{SchemaVersion: 2, EventID: fmt.Sprintf("%032x", 0xC000+e.sendN), RunID: e.runID, TaskID: taskID, AttemptID: att.ID,
		JobID: jobs[0].ID, EffectID: eff.ID, Nonce: eff.Nonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)}
	raw, _ := json.Marshal(env)
	_, err := e.c.Ingest(ctx, raw)
	return err
}

// requestChanges takes the task's current attempt through a submission and a
// review that asks for changes.
func (e *cenv) requestChanges(taskID string, n int, findings ...workflow.Finding) workflow.Attempt {
	e.t.Helper()
	e.tick()
	att := e.current(taskID)
	e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
	write(e.t, e.root, "work1.txt", fmt.Sprintf("attempt %d\n", n))
	e.settle(att)
	e.mustSend(taskID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusChangesRequested, findings...); err != nil {
		e.t.Fatal(err)
	}
	e.tick()
	return att
}

func TestRepairCreatesNewAttemptAndRejectsOldEvent(t *testing.T) {
	e := newCEnv(t)
	e.start()
	start, end := 3, 5
	old := e.requestChanges("task-1", 1,
		workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "the loop never ends", StartLine: &start, EndLine: &end},
		workflow.Finding{Severity: "low", Path: "README", Disposition: "fixed", Message: "typo in the heading"})

	cur := e.current("task-1")
	if cur.ID == old.ID || cur.Ordinal != 2 || cur.ParentAttemptID != old.ID {
		t.Fatalf("the repair must be a new attempt, linked: %+v (old %s)", cur, old.ID)
	}
	if first, _ := e.st.GetAttempt(ctx, old.ID); first.Current {
		t.Error("the old attempt is still current")
	}
	oldJobs, _ := e.st.AttemptJobs(ctx, old.ID, "builder")
	newJobs, _ := e.st.AttemptJobs(ctx, cur.ID, "builder")
	if len(oldJobs) != 1 || len(newJobs) != 1 || oldJobs[0].ID == newJobs[0].ID {
		t.Errorf("a repair gets its own builder job: %v %v", oldJobs, newJobs)
	}
	if e.builderEffect(cur).Nonce == e.builderEffect(old).Nonce {
		t.Error("a repair reuses the old nonce")
	}

	// The old attempt's builder can no longer report anything.
	for _, kind := range []string{workflow.EventBuilderSubmitted, workflow.EventBuilderStuck, workflow.EventBuilderStarted} {
		err := e.sendFor(old, "task-1", kind, "{\"message\":\"late\"}\n")
		var rej *RejectError
		if !errors.As(err, &rej) || rej.Code != "stale_attempt" {
			t.Errorf("%s from the superseded attempt: %v", kind, err)
		}
	}
	if got, _ := e.st.GetAttempt(ctx, cur.ID); got.Implementation != workflow.ImplementationPending {
		t.Errorf("the repair attempt was changed by the old one: %s", got.Implementation)
	}

	// The repair's prompt: open findings and changed files, none of the rest.
	prompt, _ := e.packet(cur)
	for _, want := range []string{"This is attempt 2", "fresh conversation", "Open findings", "[high] work1.txt:3-5: the loop never ends", "Files attempt 1 changed: work1.txt", "**Goal:** greet.", "Your task:"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the repair prompt is missing %q:\n%s", want, prompt)
		}
	}
	for _, not := range []string{"typo in the heading", "the first thing done", "done\""} {
		if strings.Contains(prompt, not) {
			t.Errorf("the repair prompt carries %q, which is not an open finding or an input", not)
		}
	}
	// It can read the review that asked for it, and nothing else of the old attempt.
	review, _, _ := e.st.LatestReviewResult(ctx, old.ID)
	if res, err := e.read(cur.ID, review, 0, 0); err != nil || !strings.Contains(res.Text, "the loop never ends") {
		t.Errorf("the repair cannot read its review: %+v %v", res, err)
	}
	if _, err := e.read(cur.ID, old.PacketHash, 0, 0); !errors.Is(err, ErrArtifactDenied) {
		t.Errorf("the repair read the old attempt's packet: %v", err)
	}
}

func TestRepairNoteIsBoundedWhateverTheReviewerWrote(t *testing.T) {
	e := newCEnv(t)
	e.start()
	var findings []workflow.Finding
	for i := 0; i < 60; i++ {
		findings = append(findings, workflow.Finding{Severity: "medium", Path: fmt.Sprintf("file%d.go", i), Disposition: "open",
			Message: strings.Repeat("a long complaint ", 80) + fmt.Sprint(i)})
	}
	old := e.requestChanges("task-1", 1, findings...)
	cur := e.current("task-1")
	prompt, m := e.packet(cur)
	if m.Estimated.Total > 8000 || len(prompt) > 8000 {
		t.Fatalf("a repair packet of %d bytes", len(prompt))
	}
	if !strings.Contains(prompt, "more open findings are in the full review") || !strings.Contains(prompt, "tandem artifact read") {
		t.Errorf("what did not fit must be named, with how to read it:\n%s", prompt)
	}
	if !strings.Contains(prompt, "file0.go") || strings.Contains(prompt, "file59.go") {
		t.Errorf("the first findings are shown and the last are not")
	}
	if strings.Contains(prompt, strings.Repeat("a long complaint ", 40)) {
		t.Error("one finding's message was not cut")
	}
	// All sixty are still there for whoever asks.
	review, _, _ := e.st.LatestReviewResult(ctx, old.ID)
	if res, err := e.read(cur.ID, review, 0, 0); err != nil || res.TotalBytes < 60*100 {
		t.Errorf("%+v %v", res.TotalBytes, err)
	}
}

func TestThirdRepairPauses(t *testing.T) {
	e := newCEnv(t)
	e.start()
	finding := workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "still wrong"}
	var attempts []workflow.Attempt
	for n := 1; n <= 3; n++ {
		attempts = append(attempts, e.requestChanges("task-1", n, finding))
	}
	if st, code := e.runState(); st != "paused" || code != "repairs_exhausted" {
		t.Fatalf("run is %s/%s", st, code)
	}
	as, _ := e.st.TaskAttempts(ctx, e.runID, "task-1")
	if len(as) != 3 {
		t.Fatalf("%d attempts: a fourth must not start", len(as))
	}
	builderJobs := 0
	for _, a := range as {
		js, _ := e.st.AttemptJobs(ctx, a.ID, "builder")
		builderJobs += len(js)
	}
	if builderJobs != 3 || len(e.effects(workflow.EffectBuilderDispatch)) != 3 || e.delivered()[workflow.EffectBuilderDispatch] != 3 {
		t.Errorf("%d builder jobs, %d dispatches, %d delivered", builderJobs, len(e.effects(workflow.EffectBuilderDispatch)), e.delivered()[workflow.EffectBuilderDispatch])
	}
	// Every review is still on record: nothing was lost to pause.
	for _, a := range attempts {
		if h, found, err := e.st.LatestReviewResult(ctx, a.ID); err != nil || !found || h == "" {
			t.Errorf("attempt %d lost its review: %v %v", a.Ordinal, found, err)
		}
	}
	// Ticking and recovering do not start another.
	e.tick()
	e.c.Recover(ctx, e.runID)
	e.tick()
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-1"); len(as) != 3 {
		t.Errorf("%d attempts after more ticks", len(as))
	}
}

func TestFreshSessionFailureDoesNotSendTask(t *testing.T) {
	e := newCEnv(t)
	e.tr.sendErr = func(d runner.Delivery) error {
		return runner.NotSent(errors.New("the builder's conversation could not be reset, so the task was not sent"))
	}
	e.start()
	for i := 0; i < 6; i++ {
		e.tick()
		e.clk.advance(10 * time.Second) // past each retry delay
	}
	if len(e.tr.sends) != 0 || e.delivered()[workflow.EffectBuilderDispatch] != 0 {
		t.Fatalf("%d prompts were sent although the session was never confirmed fresh", len(e.tr.sends))
	}
	if e.tr.attempts != 3 {
		t.Errorf("%d send attempts, want the three the retry policy allows", e.tr.attempts)
	}
	eff := e.effects(workflow.EffectBuilderDispatch)
	if len(eff) != 1 || eff[0].State != "failed" {
		t.Fatalf("%+v", eff)
	}
	if st, code := e.runState(); st != "paused" || code != "dispatch_failed" {
		t.Errorf("run is %s/%s", st, code)
	}
	if att := e.current("task-1"); att.Implementation != workflow.ImplementationPending {
		t.Errorf("the attempt started: %s", att.Implementation)
	}
	// Recovery does not turn a refusal into a send.
	e.c.Recover(ctx, e.runID)
	e.tick()
	if len(e.tr.sends) != 0 {
		t.Error("a recovered run sent the task")
	}
}

func TestAllBuilderKindsReceivePacket(t *testing.T) {
	type got struct{ prompt, manifest, kind string }
	var all []got
	for _, kind := range []string{"codex", "claude", "copilot", "opencode"} {
		e := newCEnv(t)
		run, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderKind = kind }))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		e.runID = run
		e.tick()
		att := e.current("task-1")
		prompt, m := e.packet(att)
		if len(e.tr.sends) != 1 || e.tr.sends[0].AgentKind != kind || e.tr.sends[0].PromptHash != att.PacketHash {
			t.Errorf("%s: sends %+v", kind, e.tr.sends)
		}
		// Only the checkout's location differs between these runs.
		prompt = strings.ReplaceAll(prompt, e.root, "<root>")
		prompt = strings.ReplaceAll(prompt, att.StartSnapshotHash[:12], "<snapshot>")
		mj, _ := json.Marshal(struct {
			Limit int
			Total int
		}{m.Limit, m.Estimated.Total})
		all = append(all, got{prompt, string(mj), kind})
	}
	for _, g := range all[1:] {
		if g.prompt != all[0].prompt {
			t.Errorf("%s was given a different prompt than %s", g.kind, all[0].kind)
		}
	}
	for _, want := range []string{"**Goal:** greet.", "Do the first thing.", "--started", "--report <file>"} {
		for _, g := range all {
			if !strings.Contains(g.prompt, want) {
				t.Errorf("%s: the packet lost %q", g.kind, want)
			}
		}
	}
}

func TestRestartBetweenPacketAndDeliveryDeliversOnce(t *testing.T) {
	e := newCEnv(t)
	crash := errors.New("the controller died after persisting the packet")
	e.c.hooks.afterAttemptCreated = func() error { return crash }
	run, err := e.c.Start(ctx, e.startRequest())
	if !errors.Is(err, crash) || run == "" {
		t.Fatalf("Start: %q %v", run, err)
	}
	e.runID = run
	as, _ := e.st.TaskAttempts(ctx, run, "task-1")
	if len(as) != 1 || as[0].PacketHash == "" || len(e.tr.sends) != 0 || len(e.effects(workflow.EffectBuilderDispatch)) != 0 {
		t.Fatalf("before the restart: %d attempts, %d sends", len(as), len(e.tr.sends))
	}
	packet := as[0].PacketHash

	// A new process, after the old writer's lease has expired.
	e.clk.advance(11 * time.Minute)
	e.c = e.newController()
	if err := e.c.Recover(ctx, run); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	e.tick()
	e.tick()
	as, _ = e.st.TaskAttempts(ctx, run, "task-1")
	if len(as) != 1 || as[0].PacketHash != packet {
		t.Errorf("the restart made %d attempts, or a different packet", len(as))
	}
	if len(e.tr.sends) != 1 || e.tr.sends[0].PromptHash != packet || e.delivered()[workflow.EffectBuilderDispatch] != 1 {
		t.Errorf("%d sends: the prompt must go once, and be the packet that was persisted", len(e.tr.sends))
	}
}
