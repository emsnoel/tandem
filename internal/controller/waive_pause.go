package controller

import (
	"context"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// waivablePause reports whether a pause waits only on a person's decision about
// a failed attempt, which a waiver can give. Each of these pauses holds a
// submitted attempt whose check or review did not pass.
func waivablePause(code string) bool {
	switch code {
	case pauseRepairs, workflow.PauseReviewFailed, workflow.PauseCheckFailed:
		return true
	}
	return false
}

// liftWaivedPause lets a run paused by a failed attempt go on once the
// planner's waivers cover every requirement the attempt failed. The next tick
// then accepts the task and starts the next one. A run paused for any other
// reason, or whose attempt still has an unmet requirement, stays paused.
// Verification is left to the tick: the gate asks for it after the waiver.
func (c *Controller) liftWaivedPause(ctx context.Context, runID string) error {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	if run.State != "paused" || !waivablePause(run.PauseCode) {
		return nil
	}
	att, found, err := c.drivenAttempt(ctx, run)
	if err != nil || !found || att.Implementation != workflow.ImplementationSubmitted {
		return err
	}
	f, err := c.d.State.AcceptanceFacts(ctx, att, c.observed(ctx, run, cfg))
	if err != nil {
		return err
	}
	if !(f.CheckWaiver || f.ReviewWaiver || f.ImplementationWaiver) || !f.WaiverTreeMatches {
		return nil
	}
	for _, r := range workflow.Unmet(att, f) {
		if r != "verification" {
			return nil
		}
	}
	return c.d.State.SetRunState(ctx, run.ID, "running", "")
}

// drivenAttempt finds the current attempt of the task the tick works on: the
// first unaccepted task, in plan order, that has attempts.
func (c *Controller) drivenAttempt(ctx context.Context, run state.Run) (workflow.Attempt, bool, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return workflow.Attempt{}, false, err
	}
	for _, t := range tasks {
		if t.AcceptedAttemptID != "" {
			continue
		}
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return workflow.Attempt{}, false, err
		}
		if len(attempts) == 0 {
			continue
		}
		for _, a := range attempts {
			if a.Current {
				return a, true, nil
			}
		}
		return workflow.Attempt{}, false, nil
	}
	return workflow.Attempt{}, false, nil
}

// checkWaived reports whether the planner waived the check of this attempt's
// tree, so a failed check no longer holds its acceptance.
func (c *Controller) checkWaived(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (bool, error) {
	f, err := c.d.State.AcceptanceFacts(ctx, att, c.observed(ctx, run, cfg))
	if err != nil {
		return false, err
	}
	return f.CheckWaiver && f.WaiverTreeMatches, nil
}
