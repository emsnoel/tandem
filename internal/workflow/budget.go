package workflow

import (
	"fmt"
	"math"
)

// BudgetPolicy limits what model work a run may start. The limits count input
// as BudgetInput weighs it; 0 turns a limit off. TaskInputLimit is for one task,
// across its attempts and their reviews; RunInputLimit is for the whole run. The
// two are independent.
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

// CachedInputDivisor sets how much a cached input token counts against a budget.
// A cache read costs about a tenth of fresh input, so a cached token counts as
// 1/10 of a token. This is the only place that sets the weight.
const CachedInputDivisor = 10

// BudgetInput is the input a budget counts. When the cache split is known (see
// Usage.Uncached), it is the fresh input plus the cached input divided by
// CachedInputDivisor, rounded up, and weighted is true. Otherwise it is the whole
// input, counted in full, since a discount would be a guess. With no input known
// it is 0 and false.
func BudgetInput(u Usage) (counted int64, weighted bool) {
	if fresh, ok := u.Uncached(); ok {
		cached := *u.CachedInput
		w := cached / CachedInputDivisor
		if cached%CachedInputDivisor != 0 {
			w++
		}
		return fresh + w, true
	}
	if u.Input == nil {
		return 0, false
	}
	return *u.Input, false
}

func evaluateScope(scope string, limit int64, u Usage, p BudgetPolicy) BudgetDecision {
	if limit <= 0 {
		return BudgetDecision{State: BudgetAvailable, SchedulingAllowed: true}
	}
	if u.Input != nil {
		in, weighted := BudgetInput(u)
		split := " (the cached share is not known, so every token counts in full)"
		if weighted {
			split = fmt.Sprintf(" (%d fresh and %d cached, and a cached token counts as 1/%d)", *u.Input-*u.CachedInput, *u.CachedInput, CachedInputDivisor)
		}
		switch {
		case in >= limit:
			return BudgetDecision{State: BudgetExhausted, Scope: scope, Reason: fmt.Sprintf("the %s's input counts as %d tokens%s, which reaches its limit of %d", scope, in, split, limit)}
		case u.Completeness == UsageComplete:
			if float64(in) >= p.WarnFraction*float64(limit) {
				return BudgetDecision{State: BudgetWarning, SchedulingAllowed: true, Scope: scope,
					Reason: fmt.Sprintf("the %s's input counts as %d of its %d tokens%s", scope, in, limit, split)}
			}
			return BudgetDecision{State: BudgetAvailable, SchedulingAllowed: true, Scope: scope}
		}
		return unknown(scope, p, fmt.Sprintf("the %s's usage is only partly known, at least %d of %d input tokens%s", scope, in, limit, split))
	}
	return unknown(scope, p, fmt.Sprintf("the %s's usage is not known, so its limit of %d cannot be checked", scope, limit))
}

func unknown(scope string, p BudgetPolicy, reason string) BudgetDecision {
	return BudgetDecision{State: BudgetUnknown, SchedulingAllowed: !p.PauseOnUnknown, Scope: scope, Reason: reason}
}
