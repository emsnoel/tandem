package controller

// Manual reconcile: a person fixed the tree outside the run and hands it back.
//
// The edit is snapshotted, recorded as an intervention and given to a new
// attempt from the task's original tree. The controller submits it, and it
// goes through the check, the review and any required verification like any
// attempt. Earlier evidence is not reused. A reconcile never touches the
// repair allowance or a provider budget.

import (
	"context"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Reconcile hands the run the tree as a person left it. It returns the attempt
// that will be checked and reviewed.
func (c *Controller) Reconcile(ctx context.Context, runID, actor, reason string) (workflow.Attempt, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return workflow.Attempt{}, fmt.Errorf("%w: a reconcile names who made the edit and why", workflow.ErrInvalidTransition)
	}
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return workflow.Attempt{}, err
	}
	if run.State != "paused" {
		return workflow.Attempt{}, fmt.Errorf("%w: run %s is %s; only a run paused by a failure can be reconciled", workflow.ErrInvalidTransition, runID, run.State)
	}
	if err := c.finishPendingReconcile(ctx, run, cfg); err != nil {
		return workflow.Attempt{}, err
	}
	failed, found, err := c.failedAttempt(ctx, run)
	if err != nil {
		return workflow.Attempt{}, err
	}
	if !found {
		return workflow.Attempt{}, fmt.Errorf("%w: run %s has no failed attempt to reconcile", workflow.ErrIncompleteEvidence, runID)
	}
	if err := c.writerSettled(ctx, run, failed); err != nil {
		return workflow.Attempt{}, err
	}
	baseline, err := c.taskBaseline(ctx, failed)
	if err != nil {
		return workflow.Attempt{}, err
	}
	tree, err := c.captureStart(ctx, cfg)
	if err != nil {
		return workflow.Attempt{}, err
	}
	reasonHash, err := c.put(ctx, strings.TrimSpace(reason)+"\n")
	if err != nil {
		return workflow.Attempt{}, err
	}
	task, err := c.taskOf(ctx, run, failed)
	if err != nil {
		return workflow.Attempt{}, err
	}
	id := derive(failed.ID, "reconcile")
	note := fmt.Sprintf("Attempt %d was handed over by %s after attempt %d failed. This is not a builder turn: nothing is sent to a builder. Reason: %s",
		failed.Ordinal+1, strings.TrimSpace(actor), failed.Ordinal, strings.TrimSpace(reason))
	spec, err := c.prepareAttempt(ctx, run, cfg, task, derive(id, "attempt"), failed.Ordinal+1, failed.ID, baseline,
		repairContext{Note: note, Refs: [][2]string{{"task_baseline", baseline}, {"reconciled_tree", tree}, {"reconcile_reason", reasonHash}}})
	if err != nil {
		return workflow.Attempt{}, err
	}
	fresh, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return workflow.Attempt{}, err
	}
	fence, err := c.ensureFence(ctx, run)
	if err != nil {
		return workflow.Attempt{}, err
	}
	rec, err := c.d.State.ReconcileAttempt(ctx, state.ReconcileRequest{ID: id, RunID: runID, TaskID: task.ID, FailedAttemptID: failed.ID,
		FailedVersion: failed.Version, Actor: strings.TrimSpace(actor), ReasonHash: reasonHash, TreeHash: tree,
		RunVersion: fresh.Version, LeaseFence: fence, Attempt: spec})
	if err != nil {
		return workflow.Attempt{}, err
	}
	if c.hooks.afterReconcileAttempt != nil {
		if err := c.hooks.afterReconcileAttempt(); err != nil {
			return workflow.Attempt{}, err
		}
	}
	next, err := c.d.State.GetAttempt(ctx, rec.AttemptID)
	if err != nil {
		return workflow.Attempt{}, err
	}
	return next, c.finishReconcileAttempt(ctx, fresh, cfg, next)
}

// ensureFence returns the writer fence this controller holds for the run's
// checkout. A new process of the same logical controller resumes the lease it
// already holds, as a tick does; any other owner has no fence and is refused.
func (c *Controller) ensureFence(ctx context.Context, run state.Run) (int64, error) {
	if held := c.fence(run.CheckoutID); held != 0 {
		return held, nil
	}
	lease, err := c.d.State.LeaseState(ctx, run.CheckoutID)
	if err != nil {
		return 0, err
	}
	if lease.Owner != c.owner || lease.State != "owned" {
		return 0, ErrNotWriter
	}
	c.setFence(run.CheckoutID, lease.Fence)
	return lease.Fence, nil
}

// finishPendingReconcile completes a reconcile attempt a crash left unsubmitted.
func (c *Controller) finishPendingReconcile(ctx context.Context, run state.Run, cfg runConfig) error {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.AcceptedAttemptID != "" {
			continue
		}
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return err
		}
		for _, a := range attempts {
			if !a.Current || a.Implementation != workflow.ImplementationPending || a.Version != 0 {
				continue
			}
			if _, ok, err := c.d.State.ReconcileInto(ctx, a.ID); err != nil {
				return err
			} else if ok {
				return c.finishReconcileAttempt(ctx, run, cfg, a)
			}
		}
	}
	return nil
}

// finishReconcileAttempt submits the reconcile attempt for the person, with the
// evidence the controller captured itself, and lets the run go on. Every step
// is keyed by the attempt, so running it again after a crash adds nothing.
func (c *Controller) finishReconcileAttempt(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) error {
	if att.Implementation == workflow.ImplementationPending {
		if _, err := c.captureEvidence(ctx, cfg, att); err != nil {
			return err
		}
		obs := c.observed(ctx, run, cfg)
		obs.SessionSettled = true
		if _, err := c.applyObserved(ctx, run, att, derive(att.ID, "reconcile-submitted"), workflow.EventBuilderSubmitted, att.PacketHash, obs); err != nil {
			return err
		}
	}
	if _, err := c.d.State.CancelPendingEffects(ctx, run.ID, []string{effectRecoveryDecision}); err != nil {
		return err
	}
	if cur, err := c.d.State.GetRun(ctx, run.ID); err == nil && cur.State == "paused" {
		return c.d.State.SetRunState(ctx, run.ID, "running", "")
	}
	return nil
}
