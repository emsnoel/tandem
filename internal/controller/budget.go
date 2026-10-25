package controller

// Budgets: what model work a run may still start.
//
// A run has two persisted limits on total logical input: per task (across its
// attempts and reviews) and for the whole run. Before starting model work the
// controller asks workflow.EvaluateBudget. Blocked work pauses the run as
// scheduling_blocked with its effect untouched, and raising a limit resumes it.
//
// A budget never stops a check, never stops running work, and never blocks
// accepting finished, checked and reviewed work. Limits are read from the run,
// never from the environment.

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// modelKinds are the internal effects that start model work.
var modelKinds = map[string]bool{workflow.EffectReviewDispatch: true, workflow.EffectCreateRepairAttempt: true}

func budgetPolicy(run state.Run) workflow.BudgetPolicy {
	return workflow.DefaultBudgetPolicy(run.TaskInputLimit, run.RunInputLimit)
}

// budgetDecision evaluates the budget for a task of the run from the usage as it
// stands now. A run with no limits is not measured at all.
func (c *Controller) budgetDecision(ctx context.Context, run state.Run, cfg runConfig, taskID string) (workflow.BudgetDecision, error) {
	p := budgetPolicy(run)
	if p.TaskInputLimit == 0 && p.RunInputLimit == 0 {
		return workflow.EvaluateBudget(Usage{}, Usage{}, p), nil
	}
	_, sentTasks, err := c.collectUsage(ctx, run, cfg)
	if err != nil {
		return workflow.BudgetDecision{}, err
	}
	rows, err := c.d.State.ListUsage(ctx, run.ID)
	if err != nil {
		return workflow.BudgetDecision{}, err
	}
	zero := Usage{Input: workflow.Int64(0), CachedInput: workflow.Int64(0), Output: workflow.Int64(0), Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}
	// Nothing sent means nothing used, which is zero; it is not unknown.
	task, all := zero, zero
	if len(sentTasks) > 0 {
		var taskParts, allParts []Usage
		for _, r := range rows {
			allParts = append(allParts, r.Usage)
			if r.TaskID == taskID {
				taskParts = append(taskParts, r.Usage)
			}
		}
		all = workflow.SumUsage(allParts)
		if sentTasks[taskID] {
			task = workflow.SumUsage(taskParts)
		}
	}
	return workflow.EvaluateBudget(task, all, p), nil
}

// holdForBudget decides whether model work for a task may start. If not it
// records why on the task's attempt, pauses the run and says so. A run that is
// already paused or stopping is left as it is.
func (c *Controller) holdForBudget(ctx context.Context, run state.Run, cfg runConfig, taskID string) (bool, error) {
	d, err := c.budgetDecision(ctx, run, cfg, taskID)
	if err != nil {
		return false, err
	}
	if d.SchedulingAllowed {
		return false, nil
	}
	err = c.recordBudget(ctx, run, cfg, taskID, d)
	if run.State == "running" {
		err = errors.Join(err, c.d.State.SetRunState(ctx, run.ID, "paused", workflow.PauseScheduling))
	}
	return true, err
}

// recordBudget writes a budget_changed event on the task's current attempt, or
// the run's latest if the task has none, carrying the decision and the limits.
// It is keyed by state, scope and limits, so the same decision is recorded once.
func (c *Controller) recordBudget(ctx context.Context, run state.Run, cfg runConfig, taskID string, d workflow.BudgetDecision) error {
	att, ok, err := c.budgetAttempt(ctx, run, taskID)
	if err != nil || !ok {
		return err
	}
	payload, err := c.putJSON(ctx, map[string]any{"state": d.State, "scheduling_allowed": d.SchedulingAllowed, "scope": d.Scope, "reason": d.Reason,
		"task_input_limit": run.TaskInputLimit, "run_input_limit": run.RunInputLimit})
	if err != nil {
		return err
	}
	obs := c.observed(ctx, run, cfg)
	// This records a decision; it does not release work, and checks are not
	// budget-gated, so nothing is scheduled by it.
	obs.Budget, obs.SchedulingAllowed = d.State, false
	_, err = c.applyObserved(ctx, run, att, derive(att.ID, "budget", string(d.State), d.Scope, fmt.Sprint(run.TaskInputLimit), fmt.Sprint(run.RunInputLimit)),
		workflow.EventBudgetChanged, payload, obs)
	return err
}

func (c *Controller) budgetAttempt(ctx context.Context, run state.Run, taskID string) (workflow.Attempt, bool, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return workflow.Attempt{}, false, err
	}
	var latest *workflow.Attempt
	for _, t := range tasks {
		as, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return workflow.Attempt{}, false, err
		}
		for i := range as {
			if !as[i].Current {
				continue
			}
			if t.ID == taskID {
				return as[i], true, nil
			}
			a := as[i]
			latest = &a
		}
	}
	if latest != nil {
		return *latest, true, nil
	}
	return workflow.Attempt{}, false, nil
}

// SetBudget persists a new limit for the run in total logical input tokens:
// scope "task" or "run", 0 for none. The change is recorded as an event. A run
// paused as scheduling_blocked is resumed, and the next tick starts only the
// work now allowed. It returns what the budget now says.
func (c *Controller) SetBudget(ctx context.Context, runID, scope string, limit int64) (workflow.BudgetDecision, error) {
	if scope != "task" && scope != "run" {
		return workflow.BudgetDecision{}, fmt.Errorf("%w: the budget scope is task or run, not %q", workflow.ErrInvalidTransition, scope)
	}
	if limit < 0 {
		return workflow.BudgetDecision{}, fmt.Errorf("%w: a budget limit cannot be negative", workflow.ErrInvalidTransition)
	}
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return workflow.BudgetDecision{}, err
	}
	if err := c.d.State.SetRunLimit(ctx, runID, scope, limit); err != nil {
		return workflow.BudgetDecision{}, err
	}
	if run, err = c.d.State.GetRun(ctx, runID); err != nil {
		return workflow.BudgetDecision{}, err
	}
	taskID := c.currentTask(ctx, run)
	d, err := c.budgetDecision(ctx, run, cfg, taskID)
	if err != nil {
		return d, err
	}
	if err := c.recordBudget(ctx, run, cfg, taskID, d); err != nil {
		return d, err
	}
	if run.State == "paused" && run.PauseCode == workflow.PauseScheduling {
		if err := c.d.State.SetRunState(ctx, runID, "running", ""); err != nil {
			return d, err
		}
	}
	return d, nil
}

// currentTask is the task being worked on: the first not yet accepted.
func (c *Controller) currentTask(ctx context.Context, run state.Run) string {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return ""
	}
	for _, t := range tasks {
		if t.AcceptedAttemptID == "" {
			return t.ID
		}
	}
	return ""
}

// gateInternal decides which internal effects may be claimed now. If a review or
// a repair is waiting and the budget does not allow model work, those are left
// pending, the run is paused, and only the rest are claimed.
func (c *Controller) gateInternal(ctx context.Context, run state.Run, cfg runConfig) ([]string, error) {
	p := budgetPolicy(run)
	if p.TaskInputLimit == 0 && p.RunInputLimit == 0 {
		return internalKinds, nil
	}
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range effects {
		if e.State != "pending" || !modelKinds[e.Kind] {
			continue
		}
		att, err := c.d.State.GetAttempt(ctx, e.AttemptID)
		if err != nil {
			return nil, err
		}
		hold, err := c.holdForBudget(ctx, run, cfg, att.TaskID)
		if err != nil {
			return nil, err
		}
		if !hold {
			return internalKinds, nil
		}
		var rest []string
		for _, k := range internalKinds {
			if !modelKinds[k] {
				rest = append(rest, k)
			}
		}
		return rest, nil
	}
	return internalKinds, nil
}
