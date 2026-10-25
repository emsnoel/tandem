package decision

import "testing"

// These two answer sets are real measurements on two opposite plans.
var (
	bcryptPlan = Answers{
		Shape: ShapeMechanical, ShapeConfidence: 1.00, RoutineProportion: 2.98,
		Security: 0.98, Destructive: 0.06, External: 0.02, Parallelizable: 0.40,
	}
	checkoutPlan = Answers{
		Shape: ShapeInvestigative, ShapeConfidence: 0.97, RoutineProportion: 0.31,
		Security: 0.52, Destructive: 0.07, External: 0.30, Parallelizable: 0.20,
	}
)

// The case the whole design exists for: maximally mechanical AND maximally
// security-touching at the same time. Shape must pick the posture and the
// overlay must still raise the floor.
func TestSecurityOverlayRaisesTheFloorOnAMechanicalPlan(t *testing.T) {
	got := Assess(bcryptPlan)
	if got.Outcome != OutcomeDelegate {
		t.Fatalf("want delegate for a confident mechanical plan, got %q", got.Outcome)
	}
	if got.Shape != ShapeMechanical {
		t.Fatalf("want the mechanical posture preserved, got %q", got.Shape)
	}
	if got.Floor != "high" {
		t.Fatalf("want the security overlay to raise the floor to high, got %q", got.Floor)
	}
}

func TestInvestigativePlanIsNotDelegated(t *testing.T) {
	got := Assess(checkoutPlan)
	if got.Outcome != OutcomeDontDelegate {
		t.Fatalf("want dont_delegate for investigative work, got %q", got.Outcome)
	}
	if got.Floor != "high" {
		t.Fatalf("want an investigative baseline of high, got %q", got.Floor)
	}
}

func TestLowShapeConfidenceYieldsNoAdviceAtAll(t *testing.T) {
	a := bcryptPlan
	a.ShapeConfidence = 0.79
	if got := Assess(a); got.Outcome != OutcomeNoAdvice {
		t.Fatalf("want no_advice just below the 0.80 threshold, got %q", got.Outcome)
	}
}

func TestPlainMechanicalPlanDelegatesAtLow(t *testing.T) {
	a := Answers{Shape: ShapeMechanical, ShapeConfidence: 0.95, RoutineProportion: 2.9}
	got := Assess(a)
	if got.Outcome != OutcomeDelegate || got.Floor != "low" {
		t.Fatalf("want delegate at low with no overlays, got %q at %q", got.Outcome, got.Floor)
	}
}

func TestNonRoutineMechanicalPlanIsNotDelegated(t *testing.T) {
	a := Answers{Shape: ShapeMechanical, ShapeConfidence: 0.95, RoutineProportion: 1.9}
	if got := Assess(a); got.Outcome != OutcomeDontDelegate {
		t.Fatalf("want dont_delegate below a routine proportion of 2.0, got %q", got.Outcome)
	}
}

func TestParallelIsReportedOnlyAboveItsThreshold(t *testing.T) {
	a := Answers{Shape: ShapeMechanical, ShapeConfidence: 0.95, RoutineProportion: 2.9, Parallelizable: 0.71}
	if got := Assess(a); !got.Parallel {
		t.Fatal("want parallel reported at 0.71")
	}
	a.Parallelizable = 0.69
	if got := Assess(a); got.Parallel {
		t.Fatal("want parallel withheld at 0.69")
	}
}

// Enumerating the answer space, because it is small enough to enumerate and
// the two invariants below are the ones that must never fail for any input.
func TestNoAnswerCombinationBreaksTheInvariants(t *testing.T) {
	overlays := []float64{0.0, 0.29, 0.30, 0.99}
	confs := []float64{0.0, 0.79, 0.80, 1.0}
	routines := []float64{0.0, 1.99, 2.0, 3.0}
	for _, s := range Shapes {
		for _, c := range confs {
			for _, r := range routines {
				for _, sec := range overlays {
					a := Answers{Shape: s, ShapeConfidence: c, RoutineProportion: r, Security: sec}
					got := Assess(a)

					if c < ThresholdShapeConfidence && got.Outcome != OutcomeNoAdvice {
						t.Fatalf("%+v produced %q below the confidence threshold", a, got.Outcome)
					}
					if got.Outcome == OutcomeDelegate && sec >= ThresholdOverlay && got.Floor != "high" {
						t.Fatalf("%+v delegated at %q with a live security overlay", a, got.Floor)
					}
				}
			}
		}
	}
}
