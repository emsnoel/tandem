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

// split is a complete figure of fresh and cached input, whose input count
// includes the cache.
func split(fresh, cached int64) Usage {
	return Usage{Input: Int64(fresh + cached), CachedInput: Int64(cached), Output: Int64(1), Completeness: UsageComplete, InputSemantics: SemanticsIncludesCache}
}

func TestBudgetCountsCacheReadsAtTheirWeight(t *testing.T) {
	p := DefaultBudgetPolicy(3_000_000, 0)
	// A task re-reads a large cached context many times and adds little fresh
	// input: 0.26M fresh and 8.89M cached count as 0.26M + 0.889M = 1.149M.
	d := EvaluateBudget(split(260_000, 8_890_000), used(0, UsageComplete), p)
	if d.State != BudgetAvailable || !d.SchedulingAllowed {
		t.Errorf("a cache-heavy task stopped: %+v", d)
	}
	// A task that adds a lot of fresh input still stops.
	d = EvaluateBudget(split(2_900_000, 1_000_000), used(0, UsageComplete), p)
	if d.State != BudgetExhausted || d.SchedulingAllowed || d.Scope != "task" {
		t.Errorf("a fresh-heavy task did not stop: %+v", d)
	}
	// The reason names the fresh and cached figures, so a person can decide.
	for _, want := range []string{"3000000", "2900000 fresh", "1000000 cached"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("reason %q does not name %q", d.Reason, want)
		}
	}
	// The warning uses the weighted figure too: 2.3M + 0.1 * 1M = 2.4M is 80%.
	d = EvaluateBudget(split(2_300_000, 1_000_000), used(0, UsageComplete), p)
	if d.State != BudgetWarning || !d.SchedulingAllowed || !strings.Contains(d.Reason, "2300000 fresh") || !strings.Contains(d.Reason, "1000000 cached") {
		t.Errorf("warning on weighted input: %+v", d)
	}
	if d := EvaluateBudget(split(2_299_999, 1_000_000), used(0, UsageComplete), p); d.State != BudgetAvailable {
		t.Errorf("just under the warning: %+v", d)
	}
	// The run limit counts the same way.
	if d := EvaluateBudget(used(0, UsageComplete), split(260_000, 8_890_000), DefaultBudgetPolicy(0, 3_000_000)); d.State != BudgetAvailable {
		t.Errorf("a cache-heavy run stopped: %+v", d)
	}
}

func TestBudgetCountsInputInFullWhenTheCacheSplitIsUnknown(t *testing.T) {
	p := DefaultBudgetPolicy(3_000_000, 0)
	// With mixed semantics the cached share cannot be trusted, so nothing is
	// discounted and the whole input counts.
	mixed := split(260_000, 8_890_000)
	mixed.InputSemantics = SemanticsUnknown
	d := EvaluateBudget(mixed, used(0, UsageComplete), p)
	if d.State != BudgetExhausted || d.SchedulingAllowed || !strings.Contains(d.Reason, "9150000") || !strings.Contains(d.Reason, "not known") {
		t.Errorf("unknown semantics: %+v", d)
	}
	// A partial figure is a lower bound. Below the limit it stays unknown, even
	// with a large known cached share.
	partial := split(200_000, 2_000_000)
	partial.Completeness = UsagePartial
	if d := EvaluateBudget(partial, used(0, UsageComplete), p); d.State != BudgetUnknown || d.SchedulingAllowed {
		t.Errorf("partial below the limit: %+v", d)
	}
	// A partial figure that reaches the limit, counted in full, exhausts it.
	partial = split(260_000, 8_890_000)
	partial.Completeness = UsagePartial
	if d := EvaluateBudget(partial, used(0, UsageComplete), p); d.State != BudgetExhausted || d.SchedulingAllowed || !strings.Contains(d.Reason, "9150000") {
		t.Errorf("partial at the limit: %+v", d)
	}
}

func TestBudgetInput(t *testing.T) {
	for _, c := range []struct {
		u        Usage
		want     int64
		weighted bool
	}{
		{split(100, 0), 100, true},
		{split(0, 10), 1, true},
		{split(0, 15), 2, true}, // a part of a token rounds up, never down
		{split(260_000, 8_890_000), 1_149_000, true},
		{Usage{Input: Int64(50), Completeness: UsagePartial, InputSemantics: SemanticsIncludesCache}, 50, false},
		{unknownUsage, 0, false},
	} {
		got, weighted := BudgetInput(c.u)
		if got != c.want || weighted != c.weighted {
			t.Errorf("BudgetInput(%+v) = %d, %v; want %d, %v", c.u, got, weighted, c.want, c.weighted)
		}
	}
}
