package decision

import "testing"

func TestValidateRejectsUnknownShape(t *testing.T) {
	a := Answers{Shape: "speculative", ShapeConfidence: 0.9}
	if err := a.Validate(); err == nil {
		t.Fatal("want an error for an unknown shape, got nil")
	}
}

func TestValidateRejectsOutOfRangeProbability(t *testing.T) {
	a := Answers{Shape: ShapeMechanical, ShapeConfidence: 0.9, Security: 1.4}
	if err := a.Validate(); err == nil {
		t.Fatal("want an error for a probability above 1, got nil")
	}
}

func TestValidateRejectsOutOfRangeRoutineProportion(t *testing.T) {
	a := Answers{Shape: ShapeMechanical, ShapeConfidence: 0.9, RoutineProportion: 3.5}
	if err := a.Validate(); err == nil {
		t.Fatal("want an error for a routine proportion above 3, got nil")
	}
}

func TestValidateAcceptsAWellFormedAnswer(t *testing.T) {
	a := Answers{
		Shape: ShapeMechanical, ShapeConfidence: 1.0,
		RoutineProportion: 2.98, Security: 0.98, Destructive: 0.06,
		External: 0.02, Parallelizable: 0.4,
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("want no error for the validated section 1b answer, got %v", err)
	}
}
