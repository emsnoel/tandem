// Package decision turns bounded workflow evidence into a typed staffing
// advisory. It is pure; the transport is internal/decision/jev and evidence
// collection internal/evidence.
//
// internal/gate must never reach this package: staffing advice is never
// permission to proceed (internal/gate/imports_test.go enforces it).
package decision

import "fmt"

// ContractVersion names the question set and answer shape this package
// expects. It travels in every audit record so an answer can be attributed to
// the contract that produced it.
const ContractVersion = "contract/v1"

// PolicyVersion names the threshold table and composition rules in policy.go.
// It is deliberately separate from ContractVersion: thresholds are retuned far
// more often than the questions change, and conflating them would make every
// retune look like a contract break.
const PolicyVersion = "policy/v1"

// Model is the pinned Jev version. The jev-latest alias is convenient for
// experiments and useless for reproducibility: an answer recorded today has to
// stay explainable after the alias moves.
const Model = "jev-1.13.0"

// Shape is the dominant character of the work a plan describes. It selects the
// subagent's posture, not its model; capability is the floor, computed in
// policy.go.
type Shape string

const (
	ShapeMechanical    Shape = "mechanical"
	ShapeInvestigative Shape = "investigative"
	ShapeIntegration   Shape = "integration"
)

// Shapes is the full answer space, in no meaningful order. Choice questions
// need it to build their option set; the property test needs it to enumerate.
var Shapes = []Shape{ShapeMechanical, ShapeInvestigative, ShapeIntegration}

func (s Shape) Valid() bool {
	for _, k := range Shapes {
		if k == s {
			return true
		}
	}
	return false
}

// Answers is one validated Jev response. Probability fields are Noul values in
// [0,1] with no confidence of their own. ShapeConfidence belongs to the Choice
// question, and RoutineProportion is a Score: a weighted float in [0,3].
type Answers struct {
	Shape             Shape
	ShapeConfidence   float64
	RoutineProportion float64

	Security       float64
	Destructive    float64
	External       float64
	Parallelizable float64
}

// Answer is one raw typed answer as the provider returns it. It is part of the
// contract, not the transport, so recorded answers can be replayed offline.
type Answer struct {
	Type       string  `json:"type"`
	Choice     string  `json:"choice"`
	Noul       float64 `json:"noul"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

// Validate refuses anything malformed rather than coercing it, because a zero
// value would pass every confidence threshold.
func (a Answers) Validate() error {
	if !a.Shape.Valid() {
		return fmt.Errorf("unknown shape %q", a.Shape)
	}
	for name, p := range map[string]float64{
		"shape_confidence": a.ShapeConfidence,
		"security":         a.Security,
		"destructive":      a.Destructive,
		"external":         a.External,
		"parallelizable":   a.Parallelizable,
	} {
		if p < 0 || p > 1 {
			return fmt.Errorf("%s = %v is outside [0,1]", name, p)
		}
	}
	if a.RoutineProportion < 0 || a.RoutineProportion > 3 {
		return fmt.Errorf("routine_proportion = %v is outside [0,3]", a.RoutineProportion)
	}
	return nil
}

// Outcome is what the policy decided. Three values, and NoAdvice is by far the
// most important: it is where low confidence, timeouts, malformed responses,
// stale digests and the feature being off all converge.
type Outcome string

const (
	OutcomeNoAdvice     Outcome = "no_advice"
	OutcomeDontDelegate Outcome = "dont_delegate"
	OutcomeDelegate     Outcome = "delegate"
)

// Advisory is the policy's output. Floor is empty when Outcome is NoAdvice.
type Advisory struct {
	Outcome  Outcome  `json:"outcome"`
	Shape    Shape    `json:"shape,omitempty"`
	Floor    string   `json:"floor,omitempty"`
	Parallel bool     `json:"parallel,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}
