package controller

import (
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// pausedByAnUnansweredReview plays a late review up to its pause: the check
// passed, the review prompt went to the pane, and nobody recorded the review
// within the acknowledgement window, so the run pauses as review_unknown.
func (e *cenv) pausedByAnUnansweredReview() workflow.Attempt {
	e.t.Helper()
	e.start()
	att := e.toReview("task-1", 1)
	e.clk.advance(state.AckWindow + time.Minute)
	e.tick()
	if st, code := e.runState(); st != "paused" || code != "review_unknown" {
		e.t.Fatalf("run %s/%s; want paused/review_unknown", st, code)
	}
	return att
}

// A recorded result answers the prompt a review_unknown pause waits on, however
// late, so it clears that pause and the next tick accepts the task.
func TestAReviewRecordedAfterTheWindowClearsItsPause(t *testing.T) {
	e := newCEnv(t)
	att := e.pausedByAnUnansweredReview()

	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatalf("review-record: %v", err)
	}
	if st, code := e.runState(); st != "running" || code != "" {
		t.Fatalf("run %s/%s after the late review; want running", st, code)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Fatalf("task-1 not accepted after its passed review: %+v", e.tasks()[0])
	}
}

// A review waiver overrules the review the pause waits on, so it clears too.
func TestAReviewWaiverClearsAPauseWaitingOnThatReview(t *testing.T) {
	e := newCEnv(t)
	att := e.pausedByAnUnansweredReview()

	if err := e.c.Waive(ctx, e.runID, "task-1", "review", "the reviewer's result is in its pane", "planner"); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if st, code := e.runState(); st != "running" || code != "" {
		t.Fatalf("run %s/%s after the review waiver; want running", st, code)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Fatalf("task-1 not accepted after its review was waived: %+v", e.tasks()[0])
	}
}

// A waiver of anything but the review leaves a review pause alone: the review
// it waits on is still owed.
func TestACheckWaiverLeavesAReviewPause(t *testing.T) {
	e := newCEnv(t)
	e.pausedByAnUnansweredReview()

	if err := e.c.Waive(ctx, e.runID, "task-1", "check", "the check is flaky", "planner"); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != "review_unknown" {
		t.Fatalf("run %s/%s; a check waiver cleared a review pause", st, code)
	}
}
