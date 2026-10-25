package decision

import (
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

func rank(v string) int {
	for i, e := range agent.EffortOrder {
		if e == v {
			return i
		}
	}
	return -1
}

// The property the escalate-only design rests on: an overlay may raise a
// floor and may never lower one. Checked across the whole product of the
// vocabulary rather than on examples, because a single missed pair is a
// security overlay being silently discarded.
func TestRaiseNeverLowers(t *testing.T) {
	for _, from := range agent.EffortOrder {
		for _, to := range agent.EffortOrder {
			got := raise(from, to)
			if rank(got) < rank(from) {
				t.Errorf("raise(%q, %q) = %q, which is below the starting floor", from, to, got)
			}
			if rank(got) < rank(to) {
				t.Errorf("raise(%q, %q) = %q, which is below the requested floor", from, to, got)
			}
		}
	}
}

func TestRaiseTakesTheHigher(t *testing.T) {
	if got := raise("low", "high"); got != "high" {
		t.Fatalf("want high, got %q", got)
	}
	if got := raise("max", "medium"); got != "max" {
		t.Fatalf("want max when the current floor is already higher, got %q", got)
	}
}

// An unknown value must not silently win or silently lose.
func TestRaiseTreatsUnknownAsNoInformation(t *testing.T) {
	if got := raise("low", "nonsense"); got != "low" {
		t.Fatalf("an unrecognised floor must not move the result, got %q", got)
	}
	if got := raise("nonsense", "high"); got != "high" {
		t.Fatalf("an unrecognised current floor must yield to a known one, got %q", got)
	}
}
