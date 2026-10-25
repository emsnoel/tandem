package decision

// The thresholds, and the asymmetry behind them.
//
// A recommendation that saves money needs high confidence; one that spends
// money needs low confidence. Calling a plan mechanical wrongly sends hard work
// to a weak executor and pays for rework; firing a security overlay wrongly
// costs one more expensive run.
//
// Starting values, to be recalibrated on real pairings. Routine proportion
// measured 2.98 and 0.31 on two opposite plans, so 2.0 sits in a wide gap.
const (
	ThresholdShapeConfidence = 0.80
	ThresholdOverlay         = 0.30
	ThresholdRoutine         = 2.0
	ThresholdParallel        = 0.70
)

// baselineFor is the floor a shape implies before any overlay is considered.
func baselineFor(s Shape) string {
	if s == ShapeMechanical {
		return "low"
	}
	return "high"
}

// Assess turns validated answers into an advisory. Every uncertainty path
// returns OutcomeNoAdvice, which renders as nothing, so doubt, failure and
// "off" look the same to the builder. Callers must run Answers.Validate first.
func Assess(a Answers) Advisory {
	floor := baselineFor(a.Shape)
	var reasons []string

	// Overlays raise and never lower. Order does not matter, which is the point
	// of a monotone lattice.
	if a.Security >= ThresholdOverlay {
		floor = raise(floor, "high")
		reasons = append(reasons, "touches security-sensitive code")
	}
	if a.Destructive >= ThresholdOverlay {
		floor = raise(floor, "high")
		reasons = append(reasons, "risks data loss or migration")
	}
	if a.External >= ThresholdOverlay {
		floor = raise(floor, "medium")
		reasons = append(reasons, "has external side effects")
	}

	if a.ShapeConfidence < ThresholdShapeConfidence {
		return Advisory{Outcome: OutcomeNoAdvice}
	}
	if a.Shape != ShapeMechanical {
		return Advisory{
			Outcome: OutcomeDontDelegate, Shape: a.Shape, Floor: floor,
			Reasons: append(reasons, "the work is not routine execution"),
		}
	}
	if a.RoutineProportion < ThresholdRoutine {
		return Advisory{
			Outcome: OutcomeDontDelegate, Shape: a.Shape, Floor: floor,
			Reasons: append(reasons, "too little of the plan is fully specified"),
		}
	}
	return Advisory{
		Outcome:  OutcomeDelegate,
		Shape:    a.Shape,
		Floor:    floor,
		Parallel: a.Parallelizable >= ThresholdParallel,
		Reasons:  reasons,
	}
}
