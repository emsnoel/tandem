package controller

// Where bounded recovery meets the tick. With a recovery policy, a failure is
// repaired once automatically or escalated as one incident. Without a policy
// none of this runs.

import (
	"context"
	"errors"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// recoverFailure handles a failed attempt under the run's recovery policy. It
// reports whether it changed anything. A failure that is eligible and covered
// by its allowance is repaired; every other failure is escalated, once.
func (c *Controller) recoverFailure(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (bool, error) {
	policy, on, err := c.recoveryPolicy(ctx, cfg)
	if err != nil || !on {
		return false, err
	}
	fc := c.classifyFailure(ctx, run, cfg, att)
	settled := c.writerSettled(ctx, run, att) == nil
	if policy.allows(fc.Kind) && automaticRepairEligible(fc.Kind, settled, fc.Concrete) {
		_, err := c.admitSuccessor(ctx, run, cfg, att, fc.Cause)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, state.ErrStopped):
			return false, nil // a stop holds the repair; nothing is lost
		case errors.Is(err, state.ErrRepairExhausted), errors.Is(err, state.ErrWorkMapping):
			// admitSuccessor paused the run. A person decides, once.
			return true, errors.Join(c.raiseIncident(ctx, run, cfg, att, fc, err.Error()))
		default:
			return false, err
		}
	}
	if policy.allows(fc.Kind) && !settled {
		return false, nil // the tree is not known yet; look again next tick
	}
	why := fc.Why
	switch {
	case why == "" && !policy.allows(fc.Kind):
		why = "the failure kind " + fc.Kind + " is not one the recovery policy repairs automatically"
	case why == "" && !fc.Concrete:
		why = "the failure names no concrete target to fix"
	case why == "":
		why = "the failure is not eligible for an automatic repair"
	}
	if err := c.raiseIncident(ctx, run, cfg, att, fc, why); err != nil {
		return false, err
	}
	if run.State == "running" {
		return true, c.d.State.SetRunState(ctx, run.ID, "paused", pauseRecovery)
	}
	return false, nil
}

// failedAttempt finds the current, unaccepted attempt a failure pause is about.
func (c *Controller) failedAttempt(ctx context.Context, run state.Run) (workflow.Attempt, bool, error) {
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
		for _, a := range attempts {
			if a.Current && (a.Implementation == workflow.ImplementationStuck || a.Check == workflow.CheckFailed ||
				a.Review == workflow.ReviewChangesRequested || a.Review == workflow.ReviewFailed) {
				return a, true, nil
			}
		}
	}
	return workflow.Attempt{}, false, nil
}

// recoverPaused runs once per pass for a paused run with a recovery policy:
// it finishes an interrupted reconcile, handles the failure that paused the
// run, and delivers the decision request an incident queued.
func (c *Controller) recoverPaused(ctx context.Context, run state.Run, cfg runConfig) error {
	if cfg.RecoveryPolicy == "" || run.State != "paused" {
		return nil
	}
	if err := c.finishPendingReconcile(ctx, run, cfg); err != nil {
		return err
	}
	var first error
	switch run.PauseCode {
	case workflow.PauseCheckFailed, workflow.PauseStuck, workflow.PauseReviewFailed:
		att, found, err := c.failedAttempt(ctx, run)
		if err != nil {
			return err
		}
		if found {
			if _, err := c.recoverFailure(ctx, run, cfg, att); err != nil {
				first = err
			}
		}
	}
	fresh, err := c.d.State.GetRun(ctx, run.ID)
	if err != nil {
		return errors.Join(first, err)
	}
	if fresh.State == "paused" {
		first = errors.Join(first, c.deliver(ctx, fresh, cfg, []string{effectRecoveryDecision}))
	}
	return first
}

// bindTasksToWork gives each task of a recovery run the logical work whose
// budget group holds its repair allowance. A slug that already names work in
// the collaboration binds to it, so the allowance carries over. A slug whose
// work has split is left unbound for a person, since a fresh allowance would
// reset a spent one.
func (c *Controller) bindTasksToWork(ctx context.Context, runID, collaboration string) error {
	tasks, err := c.d.State.RunTasks(ctx, runID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if bound, err := c.d.State.TaskWork(ctx, runID, t.ID); err != nil {
			return err
		} else if bound != "" {
			continue
		}
		exists, err := c.d.State.WorkAliasExists(ctx, collaboration, t.ID)
		if err != nil {
			return err
		}
		if exists {
			w, err := c.d.State.ResolveWork(ctx, collaboration, t.ID)
			if errors.Is(err, state.ErrWorkMapping) {
				continue // ambiguous or looped ancestry: left for a person
			}
			if err != nil {
				return err
			}
			if err := c.d.State.AttachWorkAlias(ctx, state.AliasRequest{CollaborationID: collaboration, LogicalWorkID: w.ID,
				Alias: "run:" + runID + ":" + t.ID, RunID: runID, TaskID: t.ID}); err != nil {
				return err
			}
			continue
		}
		workID, err := c.d.NewID()
		if err != nil {
			return err
		}
		groupID, err := c.d.NewID()
		if err != nil {
			return err
		}
		if err := c.d.State.CreateLogicalWork(ctx, state.WorkRequest{CollaborationID: collaboration, ID: workID, BudgetGroupID: groupID,
			Alias: t.ID, RunID: runID, TaskID: t.ID}); err != nil {
			return err
		}
	}
	return nil
}
