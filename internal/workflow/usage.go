package workflow

import (
	"fmt"
	"math"
)

// Completeness of a usage figure.
const (
	UsageComplete    = "complete"
	UsagePartial     = "partial"
	UsageUnavailable = "unavailable"
)

// What a provider's input count includes.
const (
	// SemanticsIncludesCache: the input count is the total logical input and the
	// cached count is the part of it served from cache.
	SemanticsIncludesCache = "includes_cache"
	// SemanticsExcludesCache: the input count leaves the cached tokens out.
	SemanticsExcludesCache = "excludes_cache"
	SemanticsUnknown       = "unknown"
)

// Usage is token use with known and unknown kept apart. A nil count is unknown,
// not zero. Input is the total logical input including cache, and CachedInput
// the cached part; the uncached part is derived only when both are known and
// the figure is complete.
type Usage struct {
	Input          *int64 `json:"input"`
	CachedInput    *int64 `json:"cached_input"`
	Output         *int64 `json:"output"`
	DurationMS     int64  `json:"duration_ms"`
	Completeness   string `json:"completeness"`
	InputSemantics string `json:"input_semantics"`
}

func badUsage(format string, a ...any) error {
	return fmt.Errorf("%w: usage: %s", ErrIncompleteEvidence, fmt.Sprintf(format, a...))
}

// ValidateUsage refuses figures that cannot be true: negative counts, more cached
// input than input, an unknown marker, a complete figure with a hole in it, or an
// unavailable one that claims counts.
func ValidateUsage(u Usage) error {
	for name, v := range map[string]*int64{"input": u.Input, "cached input": u.CachedInput, "output": u.Output} {
		if v != nil && *v < 0 {
			return badUsage("%s is negative (%d)", name, *v)
		}
	}
	switch {
	case u.DurationMS < 0:
		return badUsage("duration is negative (%d)", u.DurationMS)
	case u.Completeness != UsageComplete && u.Completeness != UsagePartial && u.Completeness != UsageUnavailable:
		return badUsage("completeness %q is not complete, partial or unavailable", u.Completeness)
	case u.InputSemantics != SemanticsIncludesCache && u.InputSemantics != SemanticsExcludesCache && u.InputSemantics != SemanticsUnknown:
		return badUsage("input_semantics %q is not includes_cache, excludes_cache or unknown", u.InputSemantics)
	case u.Input != nil && u.CachedInput != nil && *u.CachedInput > *u.Input:
		return badUsage("cached input %d is more than input %d", *u.CachedInput, *u.Input)
	case u.Completeness == UsageComplete && (u.Input == nil || u.Output == nil):
		return badUsage("a complete figure has an unknown input or output")
	case u.Completeness == UsageUnavailable && (u.Input != nil || u.CachedInput != nil || u.Output != nil):
		return badUsage("an unavailable figure carries counts")
	}
	return nil
}

// Uncached is the input that did not come from cache. It exists only for a
// complete figure whose input includes the cache and whose input and cached
// counts are both known: from a partial total, or one with unknown semantics, it
// would be a guess.
func (u Usage) Uncached() (int64, bool) {
	if u.Completeness != UsageComplete || u.InputSemantics != SemanticsIncludesCache || u.Input == nil || u.CachedInput == nil || *u.CachedInput > *u.Input {
		return 0, false
	}
	return *u.Input - *u.CachedInput, true
}

// Int64 returns a pointer to v, for building Usage.
func Int64(v int64) *int64 { return &v }

// NormalizeUsage turns what a provider reported into a Usage. input and cached
// are as the provider counted them under semantics: with excludes_cache the
// total logical input is their sum, and when the cached figure is missing that
// total cannot be known, so Input stays null. Unknown counts stay null and the
// result says it is partial. A record that cannot be true is an error.
func NormalizeUsage(input, cached, output *int64, semantics string) (Usage, error) {
	for name, v := range map[string]*int64{"input": input, "cached input": cached, "output": output} {
		if v != nil && *v < 0 {
			return Usage{}, badUsage("%s is negative (%d)", name, *v)
		}
	}
	u := Usage{Input: input, CachedInput: cached, Output: output, InputSemantics: semantics}
	if semantics == SemanticsExcludesCache {
		switch {
		case input == nil || cached == nil:
			u.Input = nil
		case *input > math.MaxInt64-*cached:
			return Usage{}, badUsage("input and cached input overflow")
		default:
			u.Input = Int64(*input + *cached)
		}
		u.InputSemantics = SemanticsIncludesCache
	}
	switch {
	case u.Input == nil && u.CachedInput == nil && u.Output == nil:
		u.Completeness = UsageUnavailable
	case u.Input != nil && u.CachedInput != nil && u.Output != nil && u.InputSemantics != SemanticsUnknown:
		u.Completeness = UsageComplete
	default:
		u.Completeness = UsagePartial
	}
	if u.Completeness == UsageComplete && u.InputSemantics == SemanticsUnknown {
		u.Completeness = UsagePartial
	}
	if err := ValidateUsage(u); err != nil {
		return Usage{}, err
	}
	return u, nil
}

// SumUsage adds figures. What is known is summed and is then a lower bound; any
// part that is partial or unavailable makes the sum partial, and if no part knows
// anything the sum is unavailable with null counts, not zero. Mixed semantics are
// reported as unknown, so nothing derived from them is trusted.
func SumUsage(parts []Usage) Usage {
	var sum Usage
	var in, cached, out int64
	var anyIn, anyCached, anyOut bool
	complete, anyKnown := true, false
	semantics := ""
	for i, p := range parts {
		if p.Input != nil {
			in, anyIn = addSat(in, *p.Input), true
		}
		if p.CachedInput != nil {
			cached, anyCached = addSat(cached, *p.CachedInput), true
		}
		if p.Output != nil {
			out, anyOut = addSat(out, *p.Output), true
		}
		sum.DurationMS = addSat(sum.DurationMS, p.DurationMS)
		if p.Completeness != UsageComplete {
			complete = false
		}
		if p.Input != nil || p.CachedInput != nil || p.Output != nil {
			anyKnown = true
		}
		switch {
		case i == 0:
			semantics = p.InputSemantics
		case semantics != p.InputSemantics:
			semantics = SemanticsUnknown
		}
	}
	if anyIn {
		sum.Input = Int64(in)
	}
	if anyCached {
		sum.CachedInput = Int64(cached)
	}
	if anyOut {
		sum.Output = Int64(out)
	}
	switch {
	case len(parts) == 0 || !anyKnown:
		sum.Completeness, sum.InputSemantics = UsageUnavailable, SemanticsUnknown
	case complete:
		sum.Completeness = UsageComplete
	default:
		sum.Completeness = UsagePartial
	}
	if sum.InputSemantics == "" {
		sum.InputSemantics = semantics
	}
	return sum
}

func addSat(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
