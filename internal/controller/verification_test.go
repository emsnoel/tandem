package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// jobByStage finds the attempt's reviewer job serving a stage.
func (e *cenv) jobByStage(att workflow.Attempt, stage string) (workflow.Job, bool) {
	e.t.Helper()
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil {
		e.t.Fatal(err)
	}
	for _, j := range jobs {
		if got, ok, _ := e.st.ReviewStage(ctx, j.ID); ok && got == stage {
			return j, true
		}
	}
	return workflow.Job{}, false
}

func (e *cenv) verifyPrompt(att workflow.Attempt) (workflow.Effect, bool) {
	e.t.Helper()
	for _, p := range e.effects(effectVerifyPrompt) {
		if p.AttemptID == att.ID {
			return p, true
		}
	}
	return workflow.Effect{}, false
}

// waiveReview waives the reviewer pane's review of the attempt. The
// pane's passing review is itself the independent check, so only an attempt
// with no review from it reaches the planner's verification.
func (e *cenv) waiveReview(att workflow.Attempt) {
	e.t.Helper()
	if err := e.c.Waive(ctx, e.runID, att.TaskID, "review", "the review is waived so the planner verifies", "tester"); err != nil {
		e.t.Fatal(err)
	}
	e.tick() // the run is running: it asks for the planner's verification
}

// ticksUntilVerificationSent runs ticks until the planner has the packet.
func (e *cenv) ticksUntilVerificationSent(att workflow.Attempt) workflow.Effect {
	e.t.Helper()
	for i := 0; i < 8; i++ {
		e.tick()
		if p, ok := e.verifyPrompt(att); ok && e.delivered()[effectVerifyPrompt] == 1 {
			return p
		}
	}
	e.t.Fatalf("the planner was not sent the verification packet: delivered %v", e.delivered())
	return workflow.Effect{}
}

// plannerAnswers plays the planner: it acknowledges the packet and submits a
// verification through the mailbox, then finishes its turn in the named
// conversation.
func (e *cenv) plannerAnswers(att workflow.Attempt, status, conversation string, findings ...workflow.Finding) {
	e.t.Helper()
	prompt, ok := e.verifyPrompt(att)
	job, jok := e.jobByStage(att, state.StageIndependentVerification)
	if !ok || !jok {
		e.t.Fatalf("attempt %s has no verification (prompt %v, job %v)", att.ID, ok, jok)
	}
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	if findings == nil {
		findings = []workflow.Finding{}
	}
	res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash,
		Status: status, Findings: findings}
	e.sendReview(att, prompt, "review_started", "{\"session_id\":\""+conversation+"\"}\n")
	e.sendReview(att, prompt, "review_result", string(encode(res)))
	e.tr.setAgent("planner-session", runner.Observation{State: runner.ObsCompleted, ExternalID: conversation})
}

func (e *cenv) acceptEvents() int { return e.eventCount(workflow.EventAcceptRequested) }

func (e *cenv) attemptsOf(taskID string) int {
	as, err := e.st.TaskAttempts(ctx, e.runID, taskID)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(as)
}

var defect = workflow.Finding{Severity: workflow.SeverityHigh, Path: "work1.txt", Disposition: workflow.DispositionOpen,
	Message: "the brief asked for a greeting; this deletes the greeting"}

// The planted defect: the review is waived, the planner's independent
// verification finds the problem. The task must not be
// accepted, and it goes to repair.
func TestIndependentVerificationBlocksAPlantedDefect(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	if e.reviewState(att) != workflow.ReviewWaived {
		t.Fatalf("the review is %s", e.reviewState(att))
	}
	// A waived review alone, however many ticks, accepts nothing.
	prompt := e.ticksUntilVerificationSent(att)
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if e.accepted() != 0 || e.acceptEvents() != 0 {
		t.Fatalf("a waived review accepted the task without independent verification (%d accepted)", e.accepted())
	}
	// The packet went to the planner, not the builder.
	var sent runner.Delivery
	for _, d := range e.tr.sends {
		if d.EffectID == prompt.ID {
			sent = d
		}
	}
	if sent.Kind != effectVerifyPrompt || sent.SessionID != "planner-session" || sent.AgentKind != "planner" {
		t.Fatalf("the verification packet was sent as %+v", sent)
	}

	e.plannerAnswers(att, workflow.ReviewStatusChangesRequested, "planner-conversation", defect)
	e.tick()
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if e.accepted() != 0 || e.acceptEvents() != 0 {
		t.Fatal("work the planner found defective was accepted")
	}
	if e.attemptsOf("task-1") != 2 || e.current("task-1").ParentAttemptID != att.ID {
		t.Fatalf("the defect did not send the task to repair: %d attempts", e.attemptsOf("task-1"))
	}
	if e.reviewState(att) != workflow.ReviewWaived {
		t.Fatalf("the review was rewritten to %s by the verification", e.reviewState(att))
	}
	if st, _ := e.runState(); st != "running" {
		t.Fatalf("run %s", st)
	}
}

func TestIndependentVerificationPassAcceptsOnce(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	e.ticksUntilVerificationSent(att)
	e.plannerAnswers(att, workflow.ReviewStatusPassed, "planner-conversation")
	for i := 0; i < 8; i++ {
		e.tick()
	}
	if e.accepted() != 1 || e.acceptEvents() != 1 || e.delivered()[workflow.EffectBuilderDispatch] != 2 {
		t.Fatalf("accepted %d, accept events %d, builder prompts %d", e.accepted(), e.acceptEvents(), e.delivered()[workflow.EffectBuilderDispatch])
	}
	if e.delivered()[effectVerifyPrompt] != 1 {
		t.Fatalf("%d verification packets for one attempt", e.delivered()[effectVerifyPrompt])
	}
	// The planner was asked for the one verification that counts and for nothing
	// else: no turn went to polling, forwarding or approving an advance.
	turns, err := e.c.plannerTurns(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	if turns != (PlannerTurns{Verification: 1}) {
		t.Fatalf("planner turns %+v, want one verification and nothing else", turns)
	}
}

func TestIndependentVerificationFailureStopsTheRun(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	e.ticksUntilVerificationSent(att)
	e.plannerAnswers(att, workflow.ReviewStatusFailed, "planner-conversation", defect)
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if st, code := e.runState(); st != "paused" || code != workflow.PauseReviewFailed || e.accepted() != 0 {
		t.Fatalf("run %s/%s, accepted %d", st, code, e.accepted())
	}
}

// A verification from the writer's own side is recorded and counts for nothing.
func TestIndependentVerificationBySelfReviewDoesNotCount(t *testing.T) {
	t.Run("the prompt reached the builder's conversation", func(t *testing.T) {
		e := newCEnv(t)
		e.startReviewHandback()
		att := e.toReview("task-1", 1)
		builderConversation := e.c.sessionOfEffect(ctx, e.builderEffect(att))
		if builderConversation == "" {
			t.Fatal("the fixture's builder has no conversation")
		}
		e.waiveReview(att)
		e.ticksUntilVerificationSent(att)
		e.plannerAnswers(att, workflow.ReviewStatusPassed, builderConversation)
		for i := 0; i < 4; i++ {
			e.tick()
		}
		if st, code := e.runState(); st != "paused" || code != pauseVerificationIdentity || e.accepted() != 0 {
			t.Fatalf("run %s/%s, accepted %d: a writer's own pass satisfied the independent stage", st, code, e.accepted())
		}
		v, _ := e.st.AttemptVerification(ctx, att.ID)
		if !v.Found || v.Distinct || v.Valid {
			t.Fatalf("verification outcome %+v", v)
		}
	})
	t.Run("the planner and the builder are one session", func(t *testing.T) {
		e := newCEnv(t)
		policy, _ := e.c.put(ctx, `{}`)
		e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy})
		e.registerPlanner(testCollab)
		id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
			r.BuilderSession, r.PlannerSession, r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = "one-session", "one-session", testCollab, 3, "mailbox", e.policyPath()
		}))
		if err != nil {
			t.Fatal(err)
		}
		e.runID = id
		att := e.toReview("task-1", 1)
		e.waiveReview(att)
		e.ticksUntilVerificationSent(att)
		e.tr.setAgent("one-session", runner.Observation{State: runner.ObsCompleted, ExternalID: "some-conversation"})
		prompt, _ := e.verifyPrompt(att)
		job, _ := e.jobByStage(att, state.StageIndependentVerification)
		cur, _ := e.st.GetAttempt(ctx, att.ID)
		res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash,
			Status: workflow.ReviewStatusPassed, Findings: []workflow.Finding{}}
		e.sendReview(att, prompt, "review_result", string(encode(res)))
		for i := 0; i < 4; i++ {
			e.tick()
		}
		if st, code := e.runState(); st != "paused" || code != pauseVerificationIdentity || e.accepted() != 0 {
			t.Fatalf("run %s/%s, accepted %d", st, code, e.accepted())
		}
	})
}

func TestIndependentVerificationStaleEvidenceIsRefused(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	e.ticksUntilVerificationSent(att)
	e.plannerAnswers(att, workflow.ReviewStatusPassed, "planner-conversation")
	e.work(99) // the checkout changes after the packet was built
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if st, code := e.runState(); st != "paused" || code != pauseSourceMutated || e.accepted() != 0 {
		t.Fatalf("run %s/%s, accepted %d: a verification of a tree that has changed counted", st, code, e.accepted())
	}
	if v, _ := e.st.AttemptVerification(ctx, att.ID); v.Found {
		t.Fatalf("a result for a changed tree was recorded: %+v", v)
	}
}

// The manual path, review-record, is for recovery and takes the same gates.
func TestIndependentVerificationManualRecordUsesTheSameGates(t *testing.T) {
	record := func(e *cenv, att workflow.Attempt, status string, mutate func(*workflow.ReviewResult)) error {
		job, _ := e.jobByStage(att, state.StageIndependentVerification)
		cur, _ := e.st.GetAttempt(ctx, att.ID)
		res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash,
			Status: status, Findings: []workflow.Finding{}}
		if mutate != nil {
			mutate(&res)
		}
		_, err := e.c.RecordReviewRaw(ctx, e.runID, encode(res))
		return err
	}
	t.Run("a distinct planner's manual pass accepts", func(t *testing.T) {
		e := newCEnv(t)
		e.startReviewHandback()
		att := e.toReview("task-1", 1)
		e.waiveReview(att)
		e.ticksUntilVerificationSent(att)
		if err := record(e, att, workflow.ReviewStatusPassed, nil); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			e.tick()
		}
		if e.accepted() != 1 {
			t.Fatalf("accepted %d", e.accepted())
		}
		if prompt, _ := e.verifyPrompt(att); func() string { p, _ := e.st.GetEffect(ctx, prompt.ID); return p.State }() != "acknowledged" {
			t.Fatal("recording the verification left its prompt unanswered")
		}
	})
	t.Run("the same stale-evidence gate applies", func(t *testing.T) {
		e := newCEnv(t)
		e.startReviewHandback()
		att := e.toReview("task-1", 1)
		e.waiveReview(att)
		e.ticksUntilVerificationSent(att)
		if err := record(e, att, workflow.ReviewStatusPassed, func(r *workflow.ReviewResult) { r.TreeHash = e.current("task-1").PatchHash }); err == nil {
			t.Fatal("a manual verification for another tree was recorded")
		}
		if err := record(e, att, workflow.ReviewStatusPassed, func(r *workflow.ReviewResult) { r.JobID = hex32(998) }); err == nil {
			t.Fatal("a manual verification naming no job was recorded")
		}
		if v, _ := e.st.AttemptVerification(ctx, att.ID); v.Found {
			t.Fatalf("a refused record left %+v", v)
		}
	})
	t.Run("a planner configured as the builder still does not count", func(t *testing.T) {
		e := newCEnv(t)
		policy, _ := e.c.put(ctx, `{}`)
		e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy})
		e.registerPlanner(testCollab)
		id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
			r.BuilderSession, r.PlannerSession, r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = "one-session", "one-session", testCollab, 3, "mailbox", e.policyPath()
		}))
		if err != nil {
			t.Fatal(err)
		}
		e.runID = id
		att := e.toReview("task-1", 1)
		e.waiveReview(att)
		e.ticksUntilVerificationSent(att)
		if err := record(e, att, workflow.ReviewStatusPassed, nil); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			e.tick()
		}
		if st, code := e.runState(); st != "paused" || code != pauseVerificationIdentity || e.accepted() != 0 {
			t.Fatalf("run %s/%s, accepted %d", st, code, e.accepted())
		}
	})
	t.Run("a builder review is recorded as before", func(t *testing.T) {
		e := newCEnv(t)
		e.startReviewHandback()
		att := e.toReview("task-1", 1)
		e.reviewerFinished(att, "reviewer-session")
		job, _ := e.jobByStage(att, state.StageBuilderReview)
		cur, _ := e.st.GetAttempt(ctx, att.ID)
		res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash,
			Status: workflow.ReviewStatusPassed, Findings: []workflow.Finding{}}
		if _, err := e.c.RecordReviewRaw(ctx, e.runID, encode(res)); err != nil {
			t.Fatal(err)
		}
		if e.reviewState(att) != workflow.ReviewPassed {
			t.Fatalf("review %s", e.reviewState(att))
		}
	})
}

// Verification is registered, held without a counted planner, and budgeted.
func TestIndependentVerificationIsRegisteredAndHeldWithoutAPlanner(t *testing.T) {
	e := newCEnv(t)
	policy, _ := e.c.put(ctx, `{}`)
	e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy})
	// No planner source is registered.
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = testCollab, 3, "mailbox", e.policyPath()
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = id
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	for i := 0; i < 6; i++ {
		e.tick()
	}
	if _, ok := e.verifyPrompt(att); !ok {
		t.Fatal("the verification was never requested")
	}
	if e.delivered()[effectVerifyPrompt] != 0 {
		t.Fatal("a verification turn that could not be counted was sent")
	}
	if st, code := e.runState(); st != "paused" || code != workflow.PauseScheduling {
		t.Fatalf("run %s/%s: an uncountable verification holds the run", st, code)
	}
	e.registerPlanner(testCollab)
	if err := e.c.ResumeHeld(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.ticksUntilVerificationSent(att)
}

func TestIndependentVerificationIsBudgetedAndCounted(t *testing.T) {
	e := newCEnv(t)
	logs := newFakeLogs(t)
	e.logs = logs
	e.c = e.newController()
	policy, _ := e.c.put(ctx, `{"enforce_budgets":true}`)
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: enforcedCollab, PolicyHash: policy, EnforceBudgets: true,
		InputCeilings: map[string]int64{"codex": 1000, "claude": 90}, OutputCeilings: map[string]int64{"codex": 1000, "claude": 100},
		InputReservations: map[string]int64{"codex": 60, "claude": 50}, OutputReservations: map[string]int64{"codex": 5, "claude": 5}}); err != nil {
		t.Fatal(err)
	}
	e.registerPlanner(enforcedCollab)
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = enforcedCollab, 3, "mailbox", e.policyPath()
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = id
	att := e.toReview("task-1", 1)
	// The review prompt was sent, so waiving the review keeps its reservation. The
	// 50 reserved for it and the 50 the verification needs exceed the 90 ceiling,
	// so verification waits until the reviewer's usage settles.
	reviewSession := "session-" + e.reviewPromptOf(att).ID[:6]
	e.waiveReview(att)
	for i := 0; i < 3; i++ {
		e.tick()
	}
	if e.delivered()[effectVerifyPrompt] != 0 {
		t.Fatal("the planner's verification was admitted while the reviewer's reservation was outstanding")
	}
	logs.append(t, reviewSession, `{"type":"assistant","uuid":"u1","sessionId":"`+reviewSession+`","message":{"id":"m1","model":"claude-a","stop_reason":"end_turn","usage":{"input_tokens":30,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`)
	if err := e.c.CollectCollaboration(ctx, enforcedCollab); err != nil {
		t.Fatal(err)
	}
	if b, err := e.st.ProviderBudget(ctx, enforcedCollab, "claude"); err != nil || b.InputReserved != 0 || b.InputSpent != 30 {
		t.Fatalf("the reviewer's admission did not settle from its usage: %+v %v", b, err)
	}
	if st, code := e.runState(); st == "paused" && code == workflow.PauseScheduling {
		if err := e.c.ResumeHeld(ctx, e.runID); err != nil {
			t.Fatal(err)
		}
	}
	prompt := e.ticksUntilVerificationSent(att)

	planner := derive("planner-source", enforcedCollab)
	flight, err := e.st.InFlightAdmissions(ctx, enforcedCollab)
	if err != nil {
		t.Fatal(err)
	}
	counted := false
	for _, a := range flight {
		if a.ID == admissionID(prompt.ID) && a.SourceID == planner {
			counted = true
		}
	}
	if !counted {
		t.Fatalf("the verification turn is not admitted against the planner source: %+v", flight)
	}
	b, err := e.st.ProviderBudget(ctx, enforcedCollab, "claude")
	if err != nil || b.InputReserved != 50 || b.OutputReserved != 5 {
		t.Fatalf("the planner's budget after the turn was admitted: %+v %v", b, err)
	}
	// A second verification would not fit under the planner's ceiling.
	if got := *b.InputCeiling - b.InputReserved - b.InputSpent; got >= 50 {
		t.Fatalf("%d input left: the fixture should leave no room for a second turn", got)
	}
}

func TestIndependentVerificationFallsBackToObservingTheEffect(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	e.c.d.Transport = struct{ runner.Transport }{e.tr} // a transport that cannot observe by name
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	prompt := e.ticksUntilVerificationSent(att)
	e.tr.set(prompt.ID, runner.Observation{State: runner.ObsCompleted, ExternalID: "planner-conversation"})
	e.plannerAnswers(att, workflow.ReviewStatusPassed, "planner-conversation")
	e.tr.agents = nil // only the per-effect observation is left
	for i := 0; i < 8; i++ {
		e.tick()
	}
	if e.accepted() != 1 {
		t.Fatalf("accepted %d", e.accepted())
	}
}

func TestIndependentVerificationStalledPlannerPausesAtItsDeadline(t *testing.T) {
	e := newCEnv(t)
	e.reviewTimeout = 2 * time.Minute
	e.c = e.newController()
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	prompt := e.ticksUntilVerificationSent(att)
	e.sendReview(att, prompt, "review_started", "{\"session_id\":\"planner-conversation\"}\n")
	e.clk.advance(5 * time.Minute)
	e.tick()
	if run := e.runRow(); run.State != "paused" || run.PauseCode != pauseReview || run.PauseJobID != prompt.JobID {
		t.Fatalf("a planner that started and went quiet: %s/%s waiting on %q", run.State, run.PauseCode, run.PauseJobID)
	}
}

func TestIndependentVerificationEveryStageIsMandatoryInThePinnedPolicy(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	run := e.runRow()
	cfg, err := e.c.config(ctx, run)
	if err != nil || cfg.AcceptancePolicy == "" {
		t.Fatalf("the run is not pinned to a policy: %+v %v", cfg, err)
	}
	policy, ok, err := e.c.policy(ctx, cfg)
	if err != nil || !ok {
		t.Fatalf("policy %v %v", ok, err)
	}
	for _, stage := range []string{stageReview, stageIndependentVerification} {
		if _, ok := policy.Stage(stage); !ok {
			t.Errorf("the pinned policy has no %s stage", stage)
		}
	}
	// A different file at the same path later changes nothing for this run.
	raw, _ := e.c.d.Artifacts.ReadAll(ctx, cfg.AcceptancePolicy, 1<<20)
	if string(raw) != goodPolicy {
		t.Fatalf("the pinned artifact is not the policy that was read at start")
	}
}

// A review prompt that was never sent used nothing: waiving the review returns
// its reservation at once, in the same write that cancels the prompt.
func TestWaivingAReviewReleasesThePromptThatWasNeverSent(t *testing.T) {
	e := newCEnv(t)
	policy, _ := e.c.put(ctx, `{"enforce_budgets":true}`)
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: enforcedCollab, PolicyHash: policy, EnforceBudgets: true,
		InputCeilings: map[string]int64{"codex": 1000, "claude": 90}, OutputCeilings: map[string]int64{"codex": 1000, "claude": 100},
		InputReservations: map[string]int64{"codex": 60, "claude": 50}, OutputReservations: map[string]int64{"codex": 5, "claude": 5}}); err != nil {
		t.Fatal(err)
	}
	e.registerPlanner(enforcedCollab)
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = enforcedCollab, 3, "mailbox", e.policyPath()
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = id
	e.tr.sendErr = func(d runner.Delivery) error {
		if d.Kind == effectReviewPrompt {
			return runner.NotSent(errors.New("the reviewer pane is busy"))
		}
		return nil
	}
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.c.Tick(ctx, e.runID) // the review prompt is admitted and its send fails
	if b, err := e.st.ProviderBudget(ctx, enforcedCollab, "claude"); err != nil || b.InputReserved != 50 {
		t.Fatalf("the unsent review prompt's reservation: %+v %v", b, err)
	}
	if got := e.delivered()[effectReviewPrompt]; got != 0 {
		t.Fatalf("%d review prompts were sent", got)
	}
	if err := e.c.Waive(ctx, e.runID, att.TaskID, "review", "waived before it was sent", "tester"); err != nil {
		t.Fatal(err)
	}
	if b, err := e.st.ProviderBudget(ctx, enforcedCollab, "claude"); err != nil || b.InputReserved != 0 || b.OutputReserved != 0 {
		t.Fatalf("a prompt that was never sent kept its reservation: %+v %v", b, err)
	}
	var released int
	if err := e.raw().QueryRow(`SELECT count(*) FROM dispatch_admissions WHERE state='released' AND provider='claude'`).Scan(&released); err != nil || released != 1 {
		t.Fatalf("%d released claude admissions, %v", released, err)
	}
}
