package triage

// Evidence identity for calibration.
//
// A calibration row is evidence only about the policy that produced it. The
// rule and thresholds, the judge's model and questions, the reviewer it is
// compared with and the exact input are each named by a hash here, so a change
// to any starts a new cohort. This schema is the triage policy's own.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/emsnoel/tandem/internal/decision"
)

// EvidenceSchema names this evidence format. Rows without it, or with another,
// are legacy and never count toward the current cohort.
const EvidenceSchema = "triage-evidence/v1"

// CapabilityVersion is the version of the reviewer adapter capabilities the
// evidence assumes. Pane review is the only mode that runs (see runner.Reviewer);
// enabling direct review changes what a review is and so changes this.
const CapabilityVersion = "review-capabilities/v1-pane"

// EvidenceIdentity says exactly what a row is evidence about. Complete means
// every part is present and the input it was judged on was kept.
type EvidenceIdentity struct {
	Schema             string `json:"schema"`
	PolicyHash         string `json:"policy_hash"`
	JudgeConfigHash    string `json:"judge_config_hash"`
	ReviewerConfigHash string `json:"reviewer_config_hash"`
	InputHash          string `json:"input_hash"`
	Complete           bool   `json:"complete"`
}

// Finished reports whether every part of the identity is present and the schema
// is this one. Complete is what was recorded; Finished is what is true now.
func (id EvidenceIdentity) Finished() bool {
	return id.Schema == EvidenceSchema && id.PolicyHash != "" && id.JudgeConfigHash != "" && id.ReviewerConfigHash != "" && id.InputHash != ""
}

// Cohort is the set of configurations rows must share to be counted together.
type Cohort struct {
	Schema, PolicyHash, JudgeConfigHash, ReviewerConfigHash string
}

// CurrentCohort is the cohort the code as it stands would produce, for a given
// reviewer configuration ("" if that is not known, which counts nothing).
func CurrentCohort(reviewerConfigHash string) Cohort {
	return Cohort{Schema: EvidenceSchema, PolicyHash: PolicyHash(),
		JudgeConfigHash: JudgeConfigHash(decision.Model, decision.ContractVersion, decision.PolicyVersion), ReviewerConfigHash: reviewerConfigHash}
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// thresholds is everything about the skip rule that changes what "would skip"
// means.
type thresholds struct {
	Rule, Capability                                                     string
	Implements, ScopeExceeded, Destructive, EvidenceSufficient, Priority float64
	MaxDiff                                                              int
}

func currentThresholds() thresholds {
	return thresholds{Rule: SkipRule, Capability: CapabilityVersion, Implements: MinImplements, ScopeExceeded: MaxScopeExceeded,
		Destructive: MaxDestructive, EvidenceSufficient: MinEvidenceSufficient, Priority: MaxPriority, MaxDiff: MaxDiffBytes}
}

func hashThresholds(t thresholds) string {
	return hashParts(EvidenceSchema, t.Rule, t.Capability, fmt.Sprint(t.Implements), fmt.Sprint(t.ScopeExceeded), fmt.Sprint(t.Destructive),
		fmt.Sprint(t.EvidenceSufficient), fmt.Sprint(t.Priority), fmt.Sprint(t.MaxDiff))
}

// PolicyHash identifies the skip policy: the schema, the rule's name, every
// threshold and the reviewer capability version. Retuning any of them changes it.
func PolicyHash() string { return hashThresholds(currentThresholds()) }

// JudgeConfigHash identifies the judge: its model, the contract and policy
// versions it speaks, and the exact questions it is asked.
func JudgeConfigHash(model, contractVersion, policyVersion string) string {
	questions, _ := json.Marshal(Questions())
	return hashParts(model, contractVersion, policyVersion, string(questions))
}

// ReviewerConfigHash identifies the reviewer a judgment is compared with: the
// agent kind, the model and the review prompt. If any is unknown the result is
// "" and the evidence is incomplete: a guess would let a different reviewer's
// outcomes pass as this one's.
func ReviewerConfigHash(kind, model, promptID string) string {
	if kind == "" || model == "" || promptID == "" {
		return ""
	}
	return hashParts("reviewer", kind, model, promptID)
}

// AuditSelected says whether a job's judgment is held out for audit: SHA-256 of
// the job id and the policy hash, read as one number, modulo 5, equal to 0. It
// depends on those two values and on nothing the judge answered, so it is the
// same whatever the verdict and is settled before there is one.
func AuditSelected(jobID, policyHash string) bool {
	sum := sha256.Sum256([]byte(jobID + policyHash))
	return new(big.Int).Mod(new(big.Int).SetBytes(sum[:]), big.NewInt(5)).Sign() == 0
}

// notes about what the report is and is not.
func uncertaintyNotes(r Report) []string {
	var n []string
	n = append(n, fmt.Sprintf("%d would-skip pairs with no misses allow a true miss rate up to about %.0f%% at 95%% confidence; reaching the minimum count is not a proof of safety.",
		r.SkipPairs, 100*r.MissRateUpper95))
	n = append(n, "This report is built from recorded rows only; the live calibration and the release decision have not been run against it, and skip stays off by default.")
	if r.Corrupt > 0 {
		n = append(n, fmt.Sprintf("%d calibration row(s) are corrupt or unreadable, so the evidence cannot be trusted and the gate is closed.", r.Corrupt))
	}
	if r.Closed {
		n = append(n, "A miss closed this cohort: a reviewer found a real change in a task the judge would have skipped. It stays closed until the policy, judge or reviewer configuration changes.")
	}
	if len(r.ExcludedReasons) > 0 {
		var parts []string
		for _, k := range sortedKeys(r.ExcludedReasons) {
			parts = append(parts, fmt.Sprintf("%s %d", k, r.ExcludedReasons[k]))
		}
		n = append(n, "Excluded: "+strings.Join(parts, ", ")+".")
	}
	return n
}
