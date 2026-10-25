package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// stageResult plays the reviewer: it starts, submits a result for the
// attempt's reviewer job through the mailbox, and returns the prompt it
// answered. The reviewer's pane is not yet settled.
func (e *cenv) stageResult(att workflow.Attempt, status string) workflow.Effect {
	e.t.Helper()
	prompts := e.effects("review_prompt")
	var prompt workflow.Effect
	for _, p := range prompts {
		if p.AttemptID == att.ID {
			prompt = p
		}
	}
	if prompt.ID == "" {
		e.t.Fatalf("attempt %s has no review prompt", att.ID)
	}
	e.sendReview(att, prompt, "review_started", "{\"session_id\":\"reviewer-session\"}\n")
	e.sendReview(att, prompt, "review_result", string(encode(e.resultFor(att, status))))
	return prompt
}

// candidateOf is the staged result of the attempt's builder review.
func (e *cenv) candidateOf(att workflow.Attempt) state.ReviewCandidate {
	e.t.Helper()
	job, ok := e.jobByStage(att, state.StageBuilderReview)
	if !ok {
		e.t.Fatalf("attempt %s has no builder review job", att.ID)
	}
	c, found, err := e.st.ReviewCandidateFor(ctx, job.ID)
	if err != nil || !found {
		e.t.Fatalf("no candidate: %v", err)
	}
	return c
}

func (e *cenv) runRow() state.Run {
	e.t.Helper()
	r, err := e.st.GetRun(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *cenv) eventCount(kind string) int {
	e.t.Helper()
	var n int
	if err := e.raw().QueryRow(`SELECT count(*) FROM events WHERE run_id=? AND kind=?`, e.runID, kind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *cenv) accepted() int {
	e.t.Helper()
	n := 0
	for _, task := range e.tasks() {
		if task.AcceptedAttemptID != "" {
			n++
		}
	}
	return n
}

// pausedByAnUnknownDelivery starts a handback run whose review prompt send is
// ambiguous, so the run pauses as review_unknown waiting on that reviewer.
func (e *cenv) pausedByAnUnknownDelivery() workflow.Attempt {
	e.t.Helper()
	e.tr.sendErr = func(d runner.Delivery) error {
		if d.Kind == effectReviewPrompt {
			return errors.New("herdr hung up in the middle of the send")
		}
		return nil
	}
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	run := e.runRow()
	prompt := e.effects("review_prompt")[0]
	if run.State != "paused" || run.PauseCode != "review_unknown" || run.PauseJobID == "" || run.PauseJobID != prompt.JobID || prompt.State != "delivery_unknown" {
		e.t.Fatalf("run %s/%s waiting on %q, prompt %s (job %q)", run.State, run.PauseCode, run.PauseJobID, prompt.State, prompt.JobID)
	}
	return att
}

func TestLateReviewAfterTheAcknowledgementWindowClearsOnlyItsPause(t *testing.T) {
	e := newCEnv(t)
	att := e.pausedByAnUnknownDelivery()
	pausedAt := e.runRow().PauseGeneration

	// Far beyond the old ten-minute acknowledgement interval nothing has moved.
	e.clk.advance(25 * time.Minute)
	e.tick()
	if st, code := e.runState(); st != "paused" || code != "review_unknown" {
		t.Fatalf("run %s/%s after the delay", st, code)
	}

	// The reviewer's result arrives. Bytes alone change nothing: the reviewer
	// is not yet seen to have finished.
	prompt := e.stageResult(att, workflow.ReviewStatusPassed)
	if got, _ := e.st.GetEffect(ctx, prompt.ID); got.State != "acknowledged" {
		t.Fatalf("a result did not resolve the unknown delivery: %s", got.State)
	}
	e.tick()
	if st, _ := e.runState(); st != "paused" || e.candidateOf(att).State != "staged" || e.eventCount("review_completed") != 0 {
		t.Fatal("a result applied before its reviewer settled")
	}

	// Settled: the next paused pass applies it, clears the pause and schedules
	// nothing in that same pass.
	e.reviewerFinished(att, "reviewer-session")
	sendsBefore, effectsBefore := e.delivered(), len(e.effects(workflow.EffectBuilderDispatch))
	e.tick()
	if st, code := e.runState(); st != "running" || code != "" {
		t.Fatalf("the matching pause was not cleared: %s/%s", st, code)
	}
	if e.candidateOf(att).State != "applied" || e.eventCount("review_completed") != 1 {
		t.Fatalf("candidate %s, %d review events", e.candidateOf(att).State, e.eventCount("review_completed"))
	}
	if got := e.delivered(); got[workflow.EffectBuilderDispatch] != sendsBefore[workflow.EffectBuilderDispatch] ||
		len(e.effects(workflow.EffectBuilderDispatch)) != effectsBefore || e.accepted() != 0 {
		t.Fatalf("the pass that applied a result also scheduled work: sends %v -> %v, accepted %d", sendsBefore, got, e.accepted())
	}
	if e.runRow().PauseGeneration != pausedAt {
		t.Fatalf("clearing a pause must not count as a new one")
	}

	// The reviewer pane's pass is the independent check: the planner is not
	// asked to verify.
	// The following passes advance once, whatever number there are.
	for i := 0; i < 8; i++ {
		e.tick()
	}
	if e.accepted() != 1 || e.delivered()[workflow.EffectBuilderDispatch] != 2 || e.eventCount("review_completed") != 1 {
		t.Fatalf("accepted %d, builder prompts %d, review events %d: it must advance exactly once", e.accepted(),
			e.delivered()[workflow.EffectBuilderDispatch], e.eventCount("review_completed"))
	}
}

func TestLateReviewNeverClearsAPauseItDidNotCause(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"a budget pause", workflow.PauseScheduling},
		{"a source mutation", pauseSourceMutated},
		{"an unrelated dispatch failure", pauseDispatch},
		{"an unknown writer", pauseWriterUnknown},
	} {
		e := newCEnv(t)
		e.startReviewHandback()
		att := e.toReview("task-1", 1)
		e.stageResult(att, workflow.ReviewStatusPassed)
		e.reviewerFinished(att, "reviewer-session")
		if err := e.st.SetRunState(ctx, e.runID, "paused", tc.code); err != nil {
			t.Fatal(err)
		}
		e.tick()
		if st, code := e.runState(); st != "paused" || code != tc.code {
			t.Errorf("%s: review success moved the run to %s/%s", tc.name, st, code)
		}
		if e.candidateOf(att).State != "applied" || e.eventCount("review_completed") != 1 {
			t.Errorf("%s: the result was not recorded: %s", tc.name, e.candidateOf(att).State)
		}
	}
}

func TestStopWinsOverAStagedReviewResult(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.stageResult(att, workflow.ReviewStatusPassed)
	e.reviewerFinished(att, "reviewer-session")
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if got := e.runRow().StopGeneration; got != 1 {
		t.Fatalf("stop generation %d after one stop", got)
	}
	sends := e.delivered()
	for i := 0; i < 6; i++ {
		e.tick()
	}
	if e.candidateOf(att).State != "staged" || e.eventCount("review_completed") != 0 {
		t.Fatalf("a stopped run applied a staged result: %s", e.candidateOf(att).State)
	}
	if st, _ := e.runState(); st == "running" {
		t.Fatal("review success resumed a stopped run")
	}
	if got := e.delivered(); got[workflow.EffectBuilderDispatch] != sends[workflow.EffectBuilderDispatch] || e.accepted() != 0 {
		t.Fatalf("a stopped run scheduled work: sends %v -> %v, accepted %d", sends, got, e.accepted())
	}
}

// raceCase injects a stop at one boundary of the application path.
type raceCase struct {
	name string
	set  func(*hooks, func() error)
	// appliedBeforeStop: the stop lands after the guarded write.
	appliedBeforeStop bool
}

var raceBoundaries = []raceCase{
	{"after the fence is read", func(h *hooks, f func() error) { h.afterReviewFence = f }, false},
	{"after the reviewer is observed", func(h *hooks, f func() error) { h.afterReviewObserved = f }, false},
	{"before the guarded write", func(h *hooks, f func() error) { h.beforeCandidateApply = f }, false},
	{"after the guarded write", func(h *hooks, f func() error) { h.afterCandidateApply = f }, true},
}

func TestReviewRaceStopAtEveryBoundary(t *testing.T) {
	for _, rc := range raceBoundaries {
		e := newCEnv(t)
		att := e.pausedByAnUnknownDelivery()
		e.stageResult(att, workflow.ReviewStatusPassed)
		e.reviewerFinished(att, "reviewer-session")
		fired := 0
		rc.set(&e.c.hooks, func() error {
			fired++
			return e.c.Stop(ctx, e.runID)
		})
		sends := e.delivered()
		e.tick()
		if fired != 1 {
			t.Fatalf("%s: the barrier fired %d times", rc.name, fired)
		}
		cand := e.candidateOf(att)
		if rc.appliedBeforeStop {
			if cand.State != "applied" {
				t.Errorf("%s: candidate %s", rc.name, cand.State)
			}
		} else if cand.State != "staged" {
			t.Errorf("%s: a stop that landed before the guarded write still let it apply (%s)", rc.name, cand.State)
		}
		if st, _ := e.runState(); st == "running" || st == "finished" {
			t.Errorf("%s: the run is %s after a stop", rc.name, st)
		}
		if e.runRow().StopGeneration != 1 {
			t.Errorf("%s: stop generation %d", rc.name, e.runRow().StopGeneration)
		}
		// Nothing new is dispatched afterwards, however many passes follow.
		for i := 0; i < 6; i++ {
			e.tick()
		}
		if got := e.delivered(); got[workflow.EffectBuilderDispatch] != sends[workflow.EffectBuilderDispatch] || e.accepted() != 0 {
			t.Errorf("%s: work was scheduled after the stop: sends %v -> %v, accepted %d", rc.name, sends, got, e.accepted())
		}
	}
}

// Anything that moves the fence between the read and the write makes the write
// a no-op. The next pass reads again and applies exactly once.
func TestReviewRaceFenceMovesAreRetriedOnce(t *testing.T) {
	moves := map[string]func(*cenv){
		"a new pause": func(e *cenv) {
			prompt := e.effects("review_prompt")[0]
			e.st.SetRunState(ctx, e.runID, "running", "")
			e.st.SetRunPausedFor(ctx, e.runID, pauseReview, prompt.JobID, prompt.ID)
		},
		"the run version": func(e *cenv) {
			if _, err := e.raw().Exec(`UPDATE runs SET version=version+1 WHERE id=?`, e.runID); err != nil {
				e.t.Fatal(err)
			}
		},
	}
	for name, move := range moves {
		e := newCEnv(t)
		att := e.pausedByAnUnknownDelivery()
		e.stageResult(att, workflow.ReviewStatusPassed)
		e.reviewerFinished(att, "reviewer-session")
		once := true
		e.c.hooks.beforeCandidateApply = func() error {
			if once {
				once = false
				move(e)
			}
			return nil
		}
		e.tick()
		if e.candidateOf(att).State != "staged" {
			t.Errorf("%s: a write under a moved fence applied (%s)", name, e.candidateOf(att).State)
		}
		if st, _ := e.runState(); st != "paused" {
			t.Errorf("%s: the run is %s", name, st)
		}
		e.tick()
		if e.candidateOf(att).State != "applied" || e.eventCount("review_completed") != 1 {
			t.Errorf("%s: the retry did not apply once: %s, %d review events", name, e.candidateOf(att).State, e.eventCount("review_completed"))
		}
		if st, _ := e.runState(); st != "running" {
			t.Errorf("%s: the retry left the run %s", name, st)
		}
	}
}

func TestReviewRaceLostLeaseNeverApplies(t *testing.T) {
	e := newCEnv(t)
	att := e.pausedByAnUnknownDelivery()
	e.stageResult(att, workflow.ReviewStatusPassed)
	e.reviewerFinished(att, "reviewer-session")
	e.c.hooks.beforeCandidateApply = func() error {
		_, err := e.raw().Exec(`UPDATE checkout_leases SET fence=fence+1`)
		return err
	}
	e.tick()
	if e.candidateOf(att).State != "staged" {
		t.Fatalf("a write by a controller whose lease moved applied: %s", e.candidateOf(att).State)
	}
	if e.delivered()[effectVerifyPrompt] != 0 {
		t.Fatal("the planner was asked to verify a task the reviewer pane passed")
	}
	if st, _ := e.runState(); st != "paused" {
		t.Fatalf("the run is %s", st)
	}
}

// A crash between recording the outcome and the guarded write converges: the
// restarted controller records the same outcome once and then applies.
func TestReviewRaceCrashBetweenRecordAndApplyConverges(t *testing.T) {
	e := newCEnv(t)
	e.owner = "the-controller"
	e.c = e.newController()
	att := e.pausedByAnUnknownDelivery()
	e.stageResult(att, workflow.ReviewStatusPassed)
	e.reviewerFinished(att, "reviewer-session")
	e.c.hooks.beforeCandidateApply = func() error { return errors.New("killed before the guarded write") }
	if err := e.c.Tick(ctx, e.runID); err == nil {
		t.Fatal("the failpoint did not fire")
	}
	if e.candidateOf(att).State != "staged" || e.eventCount("review_completed") != 1 {
		t.Fatalf("candidate %s, %d review events at the crash", e.candidateOf(att).State, e.eventCount("review_completed"))
	}
	e.c = e.newController() // the same logical writer, a new process
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatalf("recover: %v", err)
	}
	for i := 0; i < 8; i++ {
		e.tick()
	}
	if e.delivered()[effectVerifyPrompt] != 0 {
		t.Fatal("the planner was asked to verify a task the reviewer pane passed")
	}
	if e.candidateOf(att).State != "applied" || e.eventCount("review_completed") != 1 || e.accepted() != 1 || e.delivered()[workflow.EffectBuilderDispatch] != 2 {
		t.Fatalf("after the restart: candidate %s, %d review events, accepted %d, builder prompts %d", e.candidateOf(att).State,
			e.eventCount("review_completed"), e.accepted(), e.delivered()[workflow.EffectBuilderDispatch])
	}
}

// A collaboration stop that lands after the pause is cleared wins the next
// admission. Launches admitted before it stay visible as in flight.
func TestReviewRaceCollaborationStopWinsTheNextAdmission(t *testing.T) {
	e := newCEnv(t)
	e.startEnforcedHandback(1000)
	att := e.toReview("task-1", 1)
	inFlight, err := e.st.InFlightAdmissions(ctx, enforcedCollab)
	if err != nil || len(inFlight) == 0 {
		t.Fatalf("the launches before the stop are not in flight: %v %v", inFlight, err)
	}
	e.stageResult(att, workflow.ReviewStatusPassed)
	e.reviewerFinished(att, "reviewer-session")
	e.c.hooks.afterCandidateApply = func() error {
		return e.st.StopCollaboration(ctx, state.ControlRequest{CollaborationID: enforcedCollab, Actor: "user", Reason: "stop between apply and admission"})
	}
	sent := e.delivered()
	e.tick()
	if e.candidateOf(att).State != "applied" {
		t.Fatalf("candidate %s", e.candidateOf(att).State)
	}
	for i := 0; i < 6; i++ {
		e.tick()
	}
	// The launch that follows a builder review is the planner's verification.
	// It is refused at admission, and nothing else is dispatched either.
	if got := e.delivered(); got[effectVerifyPrompt] != sent[effectVerifyPrompt] || got[workflow.EffectBuilderDispatch] != sent[workflow.EffectBuilderDispatch] {
		t.Fatalf("a launch was admitted after the collaboration stop: sent %v, now %v", sent, got)
	}
	if st, code := e.runState(); st != "paused" || code != workflow.PauseScheduling {
		t.Fatalf("run %s/%s: a refused admission holds the run", st, code)
	}
	after, _ := e.st.InFlightAdmissions(ctx, enforcedCollab)
	if len(after) < len(inFlight) {
		t.Fatalf("admissions made before the stop disappeared: %d -> %d", len(inFlight), len(after))
	}
}

// A reviewer that started and then went quiet is noticed by its own deadline,
// separately from delivery. Its late result clears that pause and no other.
func TestLateReviewStalledReviewerPausesAtItsDeadline(t *testing.T) {
	e := newCEnv(t)
	e.reviewTimeout = 2 * time.Minute
	e.c = e.newController()
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	prompt := e.effects("review_prompt")[0]
	e.sendReview(att, prompt, "review_started", "{\"session_id\":\"reviewer-session\"}\n")
	e.tick()
	if st, _ := e.runState(); st != "running" {
		t.Fatalf("a review within its deadline paused the run: %s", st)
	}
	e.clk.advance(3 * time.Minute)
	e.tick()
	run := e.runRow()
	if run.State != "paused" || run.PauseCode != pauseReview || run.PauseJobID != prompt.JobID {
		t.Fatalf("a stalled reviewer: %s/%s waiting on %q", run.State, run.PauseCode, run.PauseJobID)
	}
	e.sendReview(att, prompt, "review_result", string(encode(e.resultFor(att, workflow.ReviewStatusPassed))))
	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	if st, code := e.runState(); st != "running" || code != "" {
		t.Fatalf("the late result did not clear the stall pause: %s/%s", st, code)
	}
}

func TestLateReviewInvalidOrChangedResultsAreRefusedNotApplied(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	prompt := e.effects("review_prompt")[0]
	// An object that is not a review of this job.
	if err := e.sendReviewError(att, prompt, "review_result", "{\"status\":\"passed\"}\n"); err == nil {
		t.Fatal("invalid result accepted")
	}
	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	job, _ := e.jobByStage(att, state.StageBuilderReview)
	if _, found, _ := e.st.ReviewCandidateFor(ctx, job.ID); found || e.eventCount("review_completed") != 0 {
		t.Fatal("invalid result reserved candidate slot")
	}

	if st, _ := e.runState(); st != "running" {
		t.Fatalf("an invalid result moved the run to %s", st)
	}
	// A manual review-record still works for the same attempt.
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatalf("manual recovery after a refused candidate: %v", err)
	}
}

func TestLateReviewTreeChangeAfterReviewPausesAndRefuses(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.stageResult(att, workflow.ReviewStatusPassed)
	e.reviewerFinished(att, "reviewer-session")
	e.work(99) // the checkout changes after the review
	e.tick()
	if st, code := e.runState(); st != "paused" || code != pauseSourceMutated {
		t.Fatalf("run %s/%s", st, code)
	}
	if e.candidateOf(att).State != "rejected" || e.eventCount("review_completed") != 0 {
		t.Fatalf("a result for a changed tree: %s, %d events", e.candidateOf(att).State, e.eventCount("review_completed"))
	}
}

// startEnforcedHandback starts the run in a collaboration that enforces a codex
// budget, with direct review handback.
func (e *cenv) startEnforcedHandback(inCeiling int64) {
	e.t.Helper()
	policy, err := e.c.put(ctx, `{"enforce_budgets":true}`)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: enforcedCollab, PolicyHash: policy, EnforceBudgets: true,
		InputCeilings: map[string]int64{"codex": inCeiling}, OutputCeilings: map[string]int64{"codex": 1000},
		InputReservations: map[string]int64{"codex": 60}, OutputReservations: map[string]int64{"codex": 5},
		Unlimited: []string{"claude"}}); err != nil {
		e.t.Fatal(err)
	}
	e.registerPlanner(enforcedCollab)
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = enforcedCollab, 3, "mailbox", e.policyPath()
	}))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
}
