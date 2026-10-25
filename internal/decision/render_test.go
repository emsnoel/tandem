package decision

import (
	"strings"
	"testing"
)

// The property that makes the feature safe to ship: no advice renders to
// nothing whatsoever. Not an empty block, not a marker, not a comment.
func TestNoAdviceRendersToTheEmptyString(t *testing.T) {
	if got := (Advisory{Outcome: OutcomeNoAdvice}).Render(true); got != "" {
		t.Fatalf("no_advice must render as nothing at all, got %q", got)
	}
}

func TestDelegateNamesTheFloorWhenTheAdapterCanExpressIt(t *testing.T) {
	a := Advisory{Outcome: OutcomeDelegate, Shape: ShapeMechanical, Floor: "high",
		Reasons: []string{"touches security-sensitive code"}}
	got := a.Render(true)
	if !strings.Contains(got, "high") {
		t.Fatalf("want the floor named, got %q", got)
	}
	if !strings.Contains(got, "Advisory only") {
		t.Fatalf("want the block marked advisory, got %q", got)
	}
}

// Codex cannot express a per-subagent floor, so none is named.
func TestDelegateOmitsTheFloorWhenTheAdapterCannotExpressIt(t *testing.T) {
	a := Advisory{Outcome: OutcomeDelegate, Shape: ShapeMechanical, Floor: "high"}
	got := a.Render(false)
	if strings.Contains(got, "high") {
		t.Fatalf("must not name a floor the adapter cannot honour, got %q", got)
	}
	if !strings.Contains(got, "mechanical") {
		t.Fatalf("the structural half of the advice must survive, got %q", got)
	}
}

func TestDontDelegateSaysSo(t *testing.T) {
	a := Advisory{Outcome: OutcomeDontDelegate, Shape: ShapeInvestigative, Floor: "high",
		Reasons: []string{"the work is not routine execution"}}
	got := a.Render(true)
	if !strings.Contains(got, "not") {
		t.Fatalf("want an explicit negative recommendation, got %q", got)
	}
}

// The slot markers tandem-handoff defangs must never appear in generated text,
// or defang_slots will rewrite the advisory's own words.
func TestRenderedBlockCarriesNoSlotMarkers(t *testing.T) {
	a := Advisory{Outcome: OutcomeDelegate, Shape: ShapeMechanical, Floor: "low"}
	if strings.Contains(a.Render(true), "{{") {
		t.Fatal("the rendered block must not contain slot markers")
	}
}
