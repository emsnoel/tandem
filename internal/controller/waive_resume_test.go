package controller

import (
	"fmt"
	"testing"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// pausedWithRepairsExhausted plays task-1 through three attempts that each get
// changes requested, so the run pauses as repairs_exhausted on attempt 3.
func (e *cenv) pausedWithRepairsExhausted() workflow.Attempt {
	e.t.Helper()
	e.start()
	finding := workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "wrong"}
	var att workflow.Attempt
	for n := 1; n <= 3; n++ {
		e.tick()
		att = e.current("task-1")
		e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
		write(e.t, e.root, "work1.txt", fmt.Sprintf("attempt %d\n", n))
		e.settle(att)
		e.mustSend("task-1", workflow.EventBuilderSubmitted, "{}\n")
		e.tick()
		if err := e.review(att, workflow.ReviewStatusChangesRequested, finding); err != nil {
			e.t.Fatal(err)
		}
		e.tick()
	}
	if st, code := e.runState(); st != "paused" || code != pauseRepairs {
		e.t.Fatalf("run %s/%s after three attempts; want paused/%s", st, code, pauseRepairs)
	}
	return att
}

// assertMovedOn checks that task-1 was accepted on att and the run is running
// on task-2, with no archive or new handoff.
func (e *cenv) assertMovedOn(att workflow.Attempt) {
	e.t.Helper()
	if got := e.tasks()[0].AcceptedAttemptID; got != att.ID {
		e.t.Fatalf("task-1 accepted attempt %q; want the waived attempt %s", got, att.ID)
	}
	if st, code := e.runState(); st != "running" {
		e.t.Fatalf("run %s/%s after acceptance; want running", st, code)
	}
	if next := e.current("task-2"); next.ID == "" || next.Ordinal != 1 {
		e.t.Fatalf("task-2 has no first attempt: %+v", next)
	}
}

// A review waiver on the attempt that used up the repairs is the person's
// decision the pause waits for: it lifts the pause, and the next tick accepts
// the task and starts the next one.
func TestAReviewWaiverResumesARunOutOfRepairs(t *testing.T) {
	e := newCEnv(t)
	att := e.pausedWithRepairsExhausted()

	if err := e.c.Waive(ctx, e.runID, "task-1", "review", "the findings are out of scope", "planner"); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if st, code := e.runState(); st != "running" || code != "" {
		t.Fatalf("run %s/%s after the review waiver; want running", st, code)
	}
	e.tick()
	e.assertMovedOn(att)
}

// A run whose review was waived while the pause stayed, as an older tandem
// left it, resumes with tandem build recover.
func TestRecoverResumesARunOutOfRepairsWhoseReviewWasWaived(t *testing.T) {
	e := newCEnv(t)
	att := e.pausedWithRepairsExhausted()
	if err := e.c.Waive(ctx, e.runID, "task-1", "review", "the findings are out of scope", "planner"); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if err := e.st.SetRunState(ctx, e.runID, "paused", pauseRepairs); err != nil {
		t.Fatal(err)
	}

	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatalf("recover: %v", err)
	}
	e.tick()
	e.assertMovedOn(att)
}

// A waiver that leaves the review unmet decides nothing the pause waits on.
// Neither the waiver nor a recover lifts it.
func TestACheckWaiverLeavesARunOutOfRepairsPaused(t *testing.T) {
	e := newCEnv(t)
	e.pausedWithRepairsExhausted()

	if err := e.c.Waive(ctx, e.runID, "task-1", "check", "the check is flaky", "planner"); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatalf("recover: %v", err)
	}
	e.tick()
	if st, code := e.runState(); st != "paused" || code != pauseRepairs {
		t.Fatalf("run %s/%s; a check waiver lifted a pause for a review that asked for changes", st, code)
	}
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Fatal("task-1 accepted with its review unmet")
	}
}

// A check that fails on every attempt the task has pauses the run with no
// review asked for. Waiving the check alone leaves the review owed. Waiving
// both lets the run go on.
func TestWaivingAFailedCheckAndItsReviewResumesTheRun(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.chk.exit = 1
	var att workflow.Attempt
	for n := 1; n <= state.MaxAttempts; n++ {
		e.tick()
		att = e.current("task-1")
		e.submit("task-1", 1)
	}
	e.tick()
	if st, code := e.runState(); st != "paused" || code != workflow.PauseCheckFailed {
		t.Fatalf("run %s/%s; want paused/%s", st, code, workflow.PauseCheckFailed)
	}

	if err := e.c.Waive(ctx, e.runID, "task-1", "check", "the check is broken on this host", "planner"); err != nil {
		t.Fatalf("waive check: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != workflow.PauseCheckFailed {
		t.Fatalf("run %s/%s; a check waiver alone lifted the pause with the review owed", st, code)
	}
	if err := e.c.Waive(ctx, e.runID, "task-1", "review", "I read the diff myself", "planner"); err != nil {
		t.Fatalf("waive review: %v", err)
	}
	e.chk.exit = 0
	e.tick()
	e.assertMovedOn(att)
}

// A review that failed outright pauses the run the same way. Waiving it lets
// the run go on.
func TestAReviewWaiverResumesARunPausedByAFailedReview(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	if err := e.review(att, workflow.ReviewStatusFailed); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if st, code := e.runState(); st != "paused" || code != workflow.PauseReviewFailed {
		t.Fatalf("run %s/%s; want paused/%s", st, code, workflow.PauseReviewFailed)
	}

	if err := e.c.Waive(ctx, e.runID, "task-1", "review", "the reviewer misread the task", "planner"); err != nil {
		t.Fatalf("waive: %v", err)
	}
	e.tick()
	e.assertMovedOn(att)
}
