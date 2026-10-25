package workflow

import (
	"fmt"
	"math"
)

// BudgetPolicy limits what model work a run may start. The limits count total
// logical input (cache included), as the legacy task budget did; 0 turns a limit
// off. TaskInputLimit is for one task, across its attempts and their reviews;
// RunInputLimit is for the whole run. The two are independent.
type BudgetPolicy struct {
	TaskInputLimit, RunInputLimit int64
	// WarnFraction of a limit is where a warning starts, inclusive.
	WarnFraction float64
	// PauseOnUnknown stops new model work when usage under a configured limit is
	// not known well enough to say whether the limit has been reached.
	PauseOnUnknown bool
}

// DefaultBudgetPolicy has the default warning at 80%, and pauses on unknown
// usage whenever either limit is configured. With neither configured nothing is
// ever blocked or measured.
func DefaultBudgetPolicy(taskLimit, runLimit int64) BudgetPolicy {
	return BudgetPolicy{TaskInputLimit: taskLimit, RunInputLimit: runLimit, WarnFraction: 0.8, PauseOnUnknown: taskLimit > 0 || runLimit > 0}
}

// ValidateBudgetPolicy refuses limits below zero and a warning fraction outside
// 0..1.
func ValidateBudgetPolicy(p BudgetPolicy) error {
	switch {
	case p.TaskInputLimit < 0 || p.RunInputLimit < 0:
		return fmt.Errorf("%w: budget: a limit cannot be negative", ErrInvalidTransition)
	case math.IsNaN(p.WarnFraction) || p.WarnFraction < 0 || p.WarnFraction > 1:
		return fmt.Errorf("%w: budget: the warning fraction %v is not between 0 and 1", ErrInvalidTransition, p.WarnFraction)
	}
	return nil
}

// BudgetDecision is whether new model work may be scheduled and why. Scope names
// the limit that decided it, "task" or "run", and is empty when none applies.
type BudgetDecision struct {
	State             BudgetState
	SchedulingAllowed bool
	Scope, Reason     string
}

// EvaluateBudget decides, for one task's usage and the run's, whether new
// model jobs may start. It never judges running work, checks, or whether
// finished work counts.
//
// Reaching a limit exhausts it. A lower-bound figure that already reached the
// limit exhausts it; otherwise the state is unknown, which pauses scheduling
// when the policy says so.
func EvaluateBudget(taskUsage, runUsage Usage, p BudgetPolicy) BudgetDecision {
	task := evaluateScope("task", p.TaskInputLimit, taskUsage, p)
	run := evaluateScope("run", p.RunInputLimit, runUsage, p)
	d := task
	if rank(run.State) > rank(task.State) {
		d = run
	}
	d.SchedulingAllowed = task.SchedulingAllowed && run.SchedulingAllowed
	if p.TaskInputLimit == 0 && p.RunInputLimit == 0 {
		return BudgetDecision{State: BudgetAvailable, SchedulingAllowed: true, Reason: "no budget is configured"}
	}
	return d
}

func rank(s BudgetState) int {
	switch s {
	case BudgetExhausted:
		return 3
	case BudgetUnknown:
		return 2
	case BudgetWarning:
		return 1
	}
	return 0
}

func evaluateScope(scope string, limit int64, u Usage, p BudgetPolicy) BudgetDecision {
	if limit <= 0 {
		return BudgetDecision{State: BudgetAvailable, SchedulingAllowed: true}
	}
	if u.Input != nil {
		in := *u.Input
		switch {
		case in >= limit:
			return BudgetDecision{State: BudgetExhausted, Scope: scope, Reason: fmt.Sprintf("the %s has used %d input tokens, which reaches its limit of %d", scope, in, limit)}
		case u.Completeness == UsageComplete:
			if float64(in) >= p.WarnFraction*float64(limit) {
				return BudgetDecision{State: BudgetWarning, SchedulingAllowed: true, Scope: scope,
					Reason: fmt.Sprintf("the %s has used %d of %d input tokens", scope, in, limit)}
			}
			return BudgetDecision{State: BudgetAvailable, SchedulingAllowed: true, Scope: scope}
		}
		return unknown(scope, p, fmt.Sprintf("the %s's usage is only partly known (at least %d of %d input tokens)", scope, in, limit))
	}
	return unknown(scope, p, fmt.Sprintf("the %s's usage is not known, so its limit of %d cannot be checked", scope, limit))
}

func unknown(scope string, p BudgetPolicy, reason string) BudgetDecision {
	return BudgetDecision{State: BudgetUnknown, SchedulingAllowed: !p.PauseOnUnknown, Scope: scope, Reason: reason}
}
