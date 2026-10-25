// Package evidence assembles the bounded, redacted input a decision is made
// from. It is pure: callers read the files and hand the text in.
//
// No repository, diff, git history or environment is sent. Every byte sent is
// disclosed, and irrelevant context degrades Jev's answers.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
)

// MaxPlanBytes bounds the plan text. Jev 1.13 allows 32k tokens for state plus
// the longest question; this is a deliberately conservative byte budget well
// inside it, because a plan near the limit is one edit away from a 422.
const MaxPlanBytes = 48000

const truncationMarker = "\n\n[... plan truncated by tandem: it exceeded the state budget ...]"

// Snapshot is an immutable decision input. Once built it is never edited; a
// changed plan produces a new Snapshot with a different Digest, which is how
// stale advice is detected rather than carried forward.
type Snapshot struct {
	Plan               string `json:"plan"`
	CheckCommandExists bool   `json:"check_command_exists"`
	BuilderKind        string `json:"builder_kind"`
	BuilderAutonomy    string `json:"builder_autonomy"`
	Truncated          bool   `json:"truncated"`
	Digest             string `json:"-"`
}

// Build assembles a snapshot and computes its digest. The digest covers the
// contract, policy and model versions as well as the plan, so a threshold
// change invalidates cached advice.
func Build(plan string, checkCmd bool, kind, autonomy, contractVersion, policyVersion, model string) Snapshot {
	plan = Redact(plan)
	s := Snapshot{
		Plan:               plan,
		CheckCommandExists: checkCmd,
		BuilderKind:        kind,
		BuilderAutonomy:    autonomy,
	}
	if len(plan) > MaxPlanBytes {
		s.Plan = plan[:MaxPlanBytes] + truncationMarker
		s.Truncated = true
	}

	h := sha256.New()
	for _, part := range []string{
		s.Plan, kind, autonomy, contractVersion, policyVersion, model,
	} {
		_, _ = h.Write([]byte(part))
		// A separator, so that ("ab", "c") and ("a", "bc") cannot collide.
		_, _ = h.Write([]byte{0})
	}
	if checkCmd {
		_, _ = h.Write([]byte("check"))
	}
	s.Digest = hex.EncodeToString(h.Sum(nil))
	return s
}
