package workflow

import (
	"errors"
	"strings"
	"testing"
)

func n(v int64) *int64 { return &v }

func TestUsageCachedCannotExceedInput(t *testing.T) {
	u := Usage{Input: n(100), CachedInput: n(101), Output: n(5), Completeness: UsageComplete, InputSemantics: SemanticsIncludesCache}
	if err := ValidateUsage(u); err == nil || !errors.Is(err, ErrIncompleteEvidence) || !strings.Contains(err.Error(), "cached") {
		t.Errorf("cached input over input: %v", err)
	}
	// Providers that report cache separately are normalised first, and then it can.
	if got, err := NormalizeUsage(n(100), n(101), n(5), SemanticsExcludesCache); err != nil || *got.Input != 201 || *got.CachedInput != 101 {
		t.Errorf("excludes_cache: %+v %v", got, err)
	}
	// An invalid provider record is rejected, not repaired.
	if _, err := NormalizeUsage(n(100), n(101), n(5), SemanticsIncludesCache); err == nil {
		t.Error("an includes_cache record with more cached than total input was normalised")
	}
}

func TestValidateUsageRules(t *testing.T) {
	ok := Usage{Input: n(10), CachedInput: n(4), Output: n(3), DurationMS: 5, Completeness: UsageComplete, InputSemantics: SemanticsIncludesCache}
	if err := ValidateUsage(ok); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Usage){
		"negative input":          func(u *Usage) { u.Input = n(-1) },
		"negative cached":         func(u *Usage) { u.CachedInput = n(-1) },
		"negative output":         func(u *Usage) { u.Output = n(-1) },
		"negative duration":       func(u *Usage) { u.DurationMS = -1 },
		"unknown completeness":    func(u *Usage) { u.Completeness = "mostly" },
		"unknown semantics":       func(u *Usage) { u.InputSemantics = "sometimes" },
		"complete without input":  func(u *Usage) { u.Input = nil },
		"complete without output": func(u *Usage) { u.Output = nil },
		"unavailable with counts": func(u *Usage) { u.Completeness = UsageUnavailable },
	} {
		u := ok
		mut(&u)
		if err := ValidateUsage(u); err == nil || !errors.Is(err, ErrIncompleteEvidence) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unknown stays unknown: a record may have no cached figure, and then none is derived.
	part := Usage{Input: n(10), Output: n(3), Completeness: UsagePartial, InputSemantics: SemanticsUnknown}
	if err := ValidateUsage(part); err != nil {
		t.Errorf("a partial record with no cached count: %v", err)
	}
	if _, can := part.Uncached(); can {
		t.Error("an uncached figure was derived from an unknown cached count")
	}
	if v, can := ok.Uncached(); !can || v != 6 {
		t.Errorf("uncached %d %v", v, can)
	}
	for name, u := range map[string]Usage{
		"unknown semantics": {Input: n(10), CachedInput: n(4), Output: n(1), Completeness: UsageComplete, InputSemantics: SemanticsUnknown},
		"partial":           {Input: n(10), CachedInput: n(4), Output: n(1), Completeness: UsagePartial, InputSemantics: SemanticsIncludesCache},
		"no input":          {CachedInput: n(4), Completeness: UsageUnavailable, InputSemantics: SemanticsIncludesCache},
	} {
		if _, can := u.Uncached(); can {
			t.Errorf("%s: an uncached figure was derived", name)
		}
	}
}

func TestNormalizeUsageKeepsUnknownNull(t *testing.T) {
	// No cached figure at all: input is known, cache is not, and says so.
	u, err := NormalizeUsage(n(50), nil, n(7), SemanticsIncludesCache)
	if err != nil || u.CachedInput != nil || *u.Input != 50 || u.Completeness != UsagePartial {
		t.Errorf("%+v %v", u, err)
	}
	// excludes_cache with no cached figure: total logical input cannot be known.
	u, err = NormalizeUsage(n(50), nil, n(7), SemanticsExcludesCache)
	if err != nil || u.Input != nil || u.Completeness != UsagePartial {
		t.Errorf("an unknowable total logical input must be null: %+v %v", u, err)
	}
	u, err = NormalizeUsage(nil, nil, nil, SemanticsUnknown)
	if err != nil || u.Completeness != UsageUnavailable || u.Input != nil || u.Output != nil {
		t.Errorf("%+v %v", u, err)
	}
	u, err = NormalizeUsage(n(50), n(10), n(7), SemanticsIncludesCache)
	if err != nil || u.Completeness != UsageComplete || *u.CachedInput != 10 {
		t.Errorf("%+v %v", u, err)
	}
	if _, err := NormalizeUsage(n(-5), nil, nil, SemanticsUnknown); err == nil {
		t.Error("a negative counter")
	}
}

func TestSumUsageKeepsMissingApartFromZero(t *testing.T) {
	full := func(in, c, out int64) Usage {
		return Usage{Input: n(in), CachedInput: n(c), Output: n(out), DurationMS: 10, Completeness: UsageComplete, InputSemantics: SemanticsIncludesCache}
	}
	unavailable := Usage{Completeness: UsageUnavailable, InputSemantics: SemanticsUnknown}

	got := SumUsage([]Usage{full(100, 40, 5), full(50, 10, 2)})
	if *got.Input != 150 || *got.CachedInput != 50 || *got.Output != 7 || got.DurationMS != 20 || got.Completeness != UsageComplete || got.InputSemantics != SemanticsIncludesCache {
		t.Errorf("%+v", got)
	}
	if v, ok := got.Uncached(); !ok || v != 100 {
		t.Errorf("uncached %d %v", v, ok)
	}

	// One source missing: the known part is a lower bound, the total is partial,
	// and nothing is derived from it.
	got = SumUsage([]Usage{full(100, 40, 5), unavailable})
	if got.Completeness != UsagePartial || *got.Input != 100 {
		t.Errorf("%+v", got)
	}
	if _, ok := got.Uncached(); ok {
		t.Error("a partial total produced an uncached figure")
	}
	// Everything missing: null, not zero.
	got = SumUsage([]Usage{unavailable, unavailable})
	if got.Completeness != UsageUnavailable || got.Input != nil || got.Output != nil || got.CachedInput != nil {
		t.Errorf("%+v", got)
	}
	// Nothing at all is nothing known.
	if got := SumUsage(nil); got.Completeness != UsageUnavailable || got.Input != nil {
		t.Errorf("%+v", got)
	}
	// A part that knows its input but not its cache keeps the sum of cache partial.
	noCache := Usage{Input: n(10), Output: n(1), Completeness: UsagePartial, InputSemantics: SemanticsIncludesCache}
	got = SumUsage([]Usage{full(100, 40, 5), noCache})
	if got.Completeness != UsagePartial || *got.Input != 110 {
		t.Errorf("%+v", got)
	}
	if _, ok := got.Uncached(); ok {
		t.Error("uncached derived although one part has no cache figure")
	}
	// Mixed semantics are not pretended away.
	mixed := full(1, 0, 1)
	mixed.InputSemantics = SemanticsUnknown
	if got := SumUsage([]Usage{full(100, 40, 5), mixed}); got.InputSemantics != SemanticsUnknown {
		t.Errorf("%+v", got)
	}
}
