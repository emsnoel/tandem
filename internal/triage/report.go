package triage

import (
	"math"
	"sort"
)

// Row is a judgment and its measured reviewer outcome. Nil review facts mean
// the outcome has not been measured, rather than a reviewer finding no change.
type Row struct {
	// JobID is the job both the judgment and the review belong to.
	JobID             string
	Verdict           string
	WouldSkip         bool
	Reviewed          *bool
	ReviewChangedTree *bool
	// Truncated: the judge saw a cut-off diff.
	Truncated bool
	// JudgedTree and ReviewedTree are the trees the judgment and the review were
	// of. A pair needs both, and the same.
	JudgedTree, ReviewedTree string
	// AuditSelected: held out for audit when the judgment was made.
	AuditSelected bool
	Identity      EvidenceIdentity
}

// Report counts calibration evidence for one cohort, says what was left out and
// why, and keeps the health of the evidence apart from how much there is.
type Report struct {
	Pairs           int `json:"pairs"`
	ReviewerChanged int `json:"reviewer_changed"`
	Misses          int `json:"misses"`
	SkipPairs       int `json:"skip_pairs"`
	Excluded        int `json:"excluded"`
	// AuditPairs and AuditMisses are the pairs held out for audit and the misses
	// among them.
	AuditPairs  int `json:"audit_pairs"`
	AuditMisses int `json:"audit_misses"`
	// MissingPairs are judgments in this cohort whose review has not been measured.
	MissingPairs int `json:"missing_pairs"`
	// Corrupt is how many rows could not be read or did not verify.
	Corrupt int `json:"corrupt"`
	// Healthy is false if any row is corrupt: evidence that cannot be read whole
	// is not evidence the gate may rely on.
	Healthy bool `json:"healthy"`
	// Closed: a miss closed the cohort.
	Closed          bool           `json:"closed"`
	ExcludedReasons map[string]int `json:"excluded_reasons"`
	// Classes counts pairs as true_positive (reviewer changed what the judge
	// would review), false_negative (a miss), false_positive and true_negative.
	Classes map[string]int `json:"classes"`
	// MissRateUpper95 is an upper bound on the true miss rate among would-skip
	// tasks under the usual independence assumptions. It is a bound, not a proof.
	MissRateUpper95 float64  `json:"miss_rate_upper_95"`
	Notes           []string `json:"notes"`
}

// Summarize counts the pairs that belong to the cohort and are complete. Rows
// that do not are counted under the reason they were left out. Nothing from
// another cohort is pooled, a legacy row never counts, and corrupt is how many
// rows could not be read at all.
func Summarize(rows []Row, c Cohort, corrupt int) Report {
	r := Report{Corrupt: corrupt, Healthy: corrupt == 0, ExcludedReasons: map[string]int{}, Classes: map[string]int{}}
	exclude := func(reason string) {
		r.Excluded++
		r.ExcludedReasons[reason]++
	}
	for _, row := range rows {
		id := row.Identity
		switch {
		case id.Schema != EvidenceSchema:
			exclude("legacy_record")
		case row.Verdict == "unavailable":
			exclude("judge_unavailable")
		case id.PolicyHash != c.PolicyHash || id.JudgeConfigHash != c.JudgeConfigHash || id.Schema != c.Schema:
			exclude("other_cohort")
		case row.Reviewed == nil || row.ReviewChangedTree == nil:
			r.MissingPairs++
			exclude("awaiting_review")
		case !*row.Reviewed:
			exclude("not_reviewed")
		case id.ReviewerConfigHash != c.ReviewerConfigHash:
			exclude("other_cohort")
		case !id.Complete || !id.Finished():
			exclude("incomplete_identity")
		case row.JudgedTree == "" || row.ReviewedTree == "":
			exclude("tree_unknown")
		case row.JudgedTree != row.ReviewedTree:
			exclude("tree_mismatch")
		case row.Truncated:
			exclude("truncated_input")
		default:
			r.Pairs++
			changed := *row.ReviewChangedTree
			if changed {
				r.ReviewerChanged++
			}
			class := ""
			switch {
			case row.WouldSkip && changed:
				class = "false_negative"
				r.Misses++
			case row.WouldSkip:
				class = "true_negative"
			case changed:
				class = "true_positive"
			default:
				class = "false_positive"
			}
			r.Classes[class]++
			if row.WouldSkip {
				r.SkipPairs++
			}
			if row.AuditSelected {
				r.AuditPairs++
				if class == "false_negative" {
					r.AuditMisses++
				}
			}
		}
	}
	r.Closed = r.Misses > 0
	r.MissRateUpper95 = missRateUpper95(r.SkipPairs, r.Misses)
	r.Notes = uncertaintyNotes(r)
	return r
}

// Passes requires evidence about the population the judge would skip, from
// evidence that can be read whole. Any miss keeps this cohort's gate closed
// regardless of later clean rows, and so does any corrupt row.
func (r Report) Passes() bool {
	return r.Healthy && r.SkipPairs >= 30 && r.ReviewerChanged >= 10 && r.Misses == 0
}

// missRateUpper95 bounds the true miss rate from n would-skip pairs with k
// misses: with none, the exact one-sided bound 1-0.05^(1/n); otherwise the upper
// end of the Wilson interval. With no pairs it bounds nothing, so 1.
func missRateUpper95(n, k int) float64 {
	if n <= 0 {
		return 1
	}
	if k == 0 {
		return 1 - math.Pow(0.05, 1/float64(n))
	}
	const z = 1.6448536269514722
	p, nf := float64(k)/float64(n), float64(n)
	denom := 1 + z*z/nf
	centre := p + z*z/(2*nf)
	margin := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return math.Min(1, (centre+margin)/denom)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
