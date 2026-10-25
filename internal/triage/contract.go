// Package triage judges whether a finished task still needs a reviewer.
//
// It is the counterpart to internal/decision with a different question:
// decision asks what shape a plan is before work starts; triage asks whether
// one task's change did what the task asked, so a review can be skipped when
// it plainly did.
//
// It is pure; internal/decision/jev carries the request. internal/gate must
// never reach it (internal/gate/imports_test.go enforces this).
//
// ShouldSkip is recorded against the reviewer's real finding, and the reviewer
// still runs. Nothing may act on it until at least 30 shadow judgments show no
// case where the rule would have skipped a reviewer that raised a finding. One
// such case resets the count.
package triage

import (
	"fmt"
	"math"

	"github.com/emsnoel/tandem/internal/decision"
)

// Answer keys. These are the map keys in both the request and the response, so
// they are the contract between Questions() and Parse().
const (
	QImplements  = "implements_task"
	QScope       = "scope_exceeded"
	QDestructive = "touches_destructive"
	QPriority    = "review_priority"
	QEvidence    = "evidence_sufficient"
)

// MaxDiffBytes bounds the diff that becomes state. Real per-task commits in
// this repository run from 1.6KB to 11KB, so 32KiB makes truncation rare. A
// diff past one task's worth of change is where the judgment is least
// reliable, which is when Truncated should make the answer suspect.
const MaxDiffBytes = 32768

// SkipRule names the skip rule in ShouldSkip. Calibration rows carry it, and a
// row scored under another rule is excluded rather than pooled: a looser rule
// said "would skip" about tasks this one sends to review. Change it whenever a
// threshold or an evidence requirement changes.
const SkipRule = "skip-rule/v2"

// Thresholds for the skip rule. Deliberately asymmetric: see ShouldSkip.
const (
	MinImplements    = 0.90
	MaxScopeExceeded = 0.10
	MaxDestructive   = 0.10
	// MinEvidenceSufficient and MaxPriority are initial conservative policy,
	// not empirically calibrated probabilities. Priority is a score in [0,3]:
	// at or below .25 the model sees no issue worth a reviewer's time.
	MinEvidenceSufficient = 0.90
	MaxPriority           = 0.25
)

// State is what the model is shown: one task's brief and its diff, never the
// whole plan, the working tree or history. Unlike internal/decision, the diff
// is included because it is the evidence being judged.
type State struct {
	TaskBrief          string `json:"task_brief"`
	Diff               string `json:"diff"`
	CheckCommandExists bool   `json:"check_command_exists"`
	CheckOutput        string `json:"check_output,omitempty"`
	Truncated          bool   `json:"truncated"`
}

// Answers is the parsed batch. Every field is a Noul in [0,1] except Priority,
// which is a Score: a probability-weighted float over described levels.
type Answers struct {
	Implements         float64
	ScopeExceeded      float64
	Destructive        float64
	Priority           float64
	EvidenceSufficient float64
}

// Validate refuses anything out of range rather than coercing it, for the same
// reason decision.Answers.Validate does: a coerced zero would sail past the
// thresholds that exist to stop it.
func (a Answers) Validate() error {
	bad := func(v, lo, hi float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi }
	for name, p := range map[string]float64{
		"implements":          a.Implements,
		"scope_exceeded":      a.ScopeExceeded,
		"destructive":         a.Destructive,
		"evidence_sufficient": a.EvidenceSufficient,
	} {
		if bad(p, 0, 1) {
			return fmt.Errorf("%s = %v is outside [0,1]", name, p)
		}
	}
	if bad(a.Priority, 0, 3) {
		return fmt.Errorf("review_priority = %v is outside [0,3]", a.Priority)
	}
	return nil
}

// SkipFacts are the deterministic facts a skip needs besides the model's
// answers. Callers state every one explicitly; the zero value refuses.
type SkipFacts struct {
	// CheckPassed: the project's own check command exited 0.
	CheckPassed bool
	// DiffComplete: the diff shown to the model is the exact, complete change
	// of this attempt. A HEAD-relative diff of a shared worktree cannot prove it.
	DiffComplete bool
	// Truncated: the diff was cut to MaxDiffBytes before the model saw it.
	Truncated bool
}

// Confident reports whether the model's answers clear every confidence
// threshold. It is a judgment about the answers alone; it says nothing about
// whether the evidence behind them was complete.
func (a Answers) Confident() bool {
	return a.Validate() == nil &&
		a.Implements >= MinImplements &&
		a.ScopeExceeded <= MaxScopeExceeded &&
		a.Destructive <= MaxDestructive &&
		a.EvidenceSufficient >= MinEvidenceSufficient &&
		a.Priority <= MaxPriority
}

// ShouldSkip reports whether the review can be skipped. Every clause must hold.
// A wrongly started review costs tokens while a wrongly skipped one ships a
// defect, so the thresholds are asymmetric and the zero value refuses. The
// facts come from the controller: a red check or an incomplete or truncated
// diff skips nothing.
func (a Answers) ShouldSkip(f SkipFacts) bool {
	return f.CheckPassed && f.DiffComplete && !f.Truncated && a.Confident()
}

// Parse turns a raw batch into triage answers. Anything missing, of the wrong
// type or out of range is an error, never a zero value.
func Parse(raw map[string]decision.Answer) (Answers, error) {
	need := func(key, wantType string) (decision.Answer, error) {
		a, ok := raw[key]
		if !ok {
			return decision.Answer{}, fmt.Errorf("answer %q missing from the batch", key)
		}
		if a.Type != wantType {
			return decision.Answer{}, fmt.Errorf("answer %q has type %q, want %q", key, a.Type, wantType)
		}
		return a, nil
	}

	impl, err := need(QImplements, "noul")
	if err != nil {
		return Answers{}, err
	}
	scope, err := need(QScope, "noul")
	if err != nil {
		return Answers{}, err
	}
	des, err := need(QDestructive, "noul")
	if err != nil {
		return Answers{}, err
	}
	prio, err := need(QPriority, "score")
	if err != nil {
		return Answers{}, err
	}
	ev, err := need(QEvidence, "noul")
	if err != nil {
		return Answers{}, err
	}

	out := Answers{
		Implements:         impl.Noul,
		ScopeExceeded:      scope.Noul,
		Destructive:        des.Noul,
		Priority:           prio.Score,
		EvidenceSufficient: ev.Noul,
	}
	if err := out.Validate(); err != nil {
		return Answers{}, err
	}
	return out, nil
}
