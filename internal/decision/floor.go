package decision

import "github.com/emsnoel/tandem/internal/agent"

// rankOf returns a value's position in the canonical effort ordering, or -1
// when it is not in the vocabulary at all.
func rankOf(v string) int {
	for i, e := range agent.EffortOrder {
		if e == v {
			return i
		}
	}
	return -1
}

// raise returns the higher of two effort levels. Every caller raises and none
// lowers, so a confident "mechanical" can never outvote a security overlay. An
// unrecognised value carries no information: raise("low", "nonsense") is "low",
// and raise("nonsense", "high") is "high".
func raise(current, floor string) string {
	c, f := rankOf(current), rankOf(floor)
	if f > c {
		return floor
	}
	if c >= 0 {
		return current
	}
	return floor
}
