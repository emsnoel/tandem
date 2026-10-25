package workflow

import (
	"strings"
	"testing"
)

func used(in int64, completeness string) Usage {
	u := Usage{Input: Int64(in), CachedInput: Int64(0), Output: Int64(1), Completeness: completeness, InputSemantics: SemanticsIncludesCache}
	if completeness == UsagePartial {
		u.CachedInput = nil
	}
	return u
}

var unknownUsage = Usage{Completeness: UsageUnavailable, InputSemantics: SemanticsUnknown}

func TestBudgetThresholdsAreInclusive(t *testing.T) {
	p := DefaultBudgetPolicy(0, 1000)
	for _, c := range []struct {
		in      int64
		state   BudgetState
		allowed bool
	}{
		{0, BudgetAvailable, true},
		{799, BudgetAvailable, true},
		{800, BudgetWarning, true}, // the warning starts at 80% and includes it
		{999, BudgetWarning, true},
		{1000, BudgetExhausted, false}, // reaching the limit is exhausting it
		{5000, BudgetExhausted, false},
	} {
		d := EvaluateBudget(used(0, UsageComplete), used(c.in, UsageComplete), p)
		if d.State != c.state || d.SchedulingAllowed != c.allowed {
			t.Errorf("%d of 1000: %+v, want %s allowed=%v", c.in, d, c.state, c.allowed)
		}
	}
	// A different warning fraction is honoured, and 0 disables a limit.
	p.WarnFraction = 0.5
	if d := EvaluateBudget(used(0, UsageComplete), used(500, UsageComplete), p); d.State != BudgetWarning {
		t.Errorf("%+v", d)
	}
	if d := EvaluateBudget(used(0, UsageComplete), used(10_000_000, UsageComplete), DefaultBudgetPolicy(0, 0)); d.State != BudgetAvailable || !d.SchedulingAllowed || !strings.Contains(d.Reason, "no budget") {
		t.Errorf("no limit: %+v", d)
	}
}

func TestBudgetTaskAndRunLimitsIndependent(t *testing.T) {
	p := DefaultBudgetPolicy(500, 1000)
	// The task is well inside its limit and the run has used all of its own.
	d := EvaluateBudget(used(100, UsageComplete), used(1000, UsageComplete), p)
	if d.State != BudgetExhausted || d.SchedulingAllowed || d.Scope != "run" || !strings.Contains(d.Reason, "run") {
		t.Errorf("run exhausted: %+v", d)
	}
	// The run has room and the task has used its own.
	d = EvaluateBudget(used(500, UsageComplete), used(600, UsageComplete), p)
	if d.State != BudgetExhausted || d.SchedulingAllowed || d.Scope != "task" {
		t.Errorf("task exhausted: %+v", d)
	}
	// One limit alone is configured: the other scope is never consulted.
	if d := EvaluateBudget(used(10_000, UsageComplete), used(10, UsageComplete), DefaultBudgetPolicy(0, 1000)); !d.SchedulingAllowed {
		t.Errorf("an unconfigured task limit blocked work: %+v", d)
	}
	if d := EvaluateBudget(used(10, UsageComplete), used(10_000, UsageComplete), DefaultBudgetPolicy(500, 0)); !d.SchedulingAllowed {
		t.Errorf("an unconfigured run limit blocked work: %+v", d)
	}
	// The worse of the two states wins.
	d = EvaluateBudget(used(450, UsageComplete), used(100, UsageComplete), p)
	if d.State != BudgetWarning || d.Scope != "task" || !d.SchedulingAllowed {
		t.Errorf("%+v", d)
	}
}

func TestBudgetUnknownWithLimitPauses(t *testing.T) {
	p := DefaultBudgetPolicy(0, 1000)
	if !p.PauseOnUnknown || p.WarnFraction != 0.8 {
		t.Fatalf("defaults %+v", p)
	}
	for name, run := range map[string]Usage{
		"nothing known":   unknownUsage,
		"partial counts":  used(300, UsagePartial), // a lower bound, with the rest not counted
		"no input at all": {Output: Int64(5), Completeness: UsagePartial, InputSemantics: SemanticsUnknown},
	} {
		d := EvaluateBudget(used(0, UsageComplete), run, p)
		if d.State != BudgetUnknown || d.SchedulingAllowed || d.Scope != "run" || d.Reason == "" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// A lower bound that already reaches the limit is known to be over it.
	if d := EvaluateBudget(used(0, UsageComplete), used(1200, UsagePartial), p); d.State != BudgetExhausted || d.SchedulingAllowed {
		t.Errorf("a partial total that already exceeds the limit: %+v", d)
	}
	// With the policy relaxed, unknown is recorded but does not stop work.
	p.PauseOnUnknown = false
	if d := EvaluateBudget(used(0, UsageComplete), unknownUsage, p); d.State != BudgetUnknown || !d.SchedulingAllowed {
		t.Errorf("%+v", d)
	}
	// An unlimited run with unknown usage stays usable and is not called unknown:
	// there is nothing to measure it against.
	if d := EvaluateBudget(unknownUsage, unknownUsage, DefaultBudgetPolicy(0, 0)); !d.SchedulingAllowed || d.State != BudgetAvailable {
		t.Errorf("%+v", d)
	}
	// Unknown in a scope that has no limit does not count against the one that does.
	if d := EvaluateBudget(unknownUsage, used(10, UsageComplete), DefaultBudgetPolicy(0, 1000)); !d.SchedulingAllowed {
		t.Errorf("%+v", d)
	}
}

func TestValidateBudgetPolicy(t *testing.T) {
	for name, p := range map[string]BudgetPolicy{
		"negative task limit": {TaskInputLimit: -1, WarnFraction: 0.8},
		"negative run limit":  {RunInputLimit: -1, WarnFraction: 0.8},
		"warn above one":      {WarnFraction: 1.5},
		"warn negative":       {WarnFraction: -0.1},
	} {
		if err := ValidateBudgetPolicy(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidateBudgetPolicy(DefaultBudgetPolicy(0, 0)); err != nil {
		t.Error(err)
	}
}
