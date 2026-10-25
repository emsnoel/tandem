package gate

import "strings"

// ReviewScope says how much of a plan the next review must read: "full", or
// "diff" (only what changed since the newest review, checked against its
// objections).
//
// A diff needs a review of this plan, both plan texts, an actual change (an
// unchanged plan asked again gets a full look), and the same builder kind and
// autonomy, since class-4 findings depend on them. Past about half the plan
// rewritten, a full review is easier to read.
func ReviewScope(in ReviewInput) string {
	switch {
	case !in.Found, !in.PlanMeasured, in.PlanNow == in.PlanReviewed:
		return "full"
	case !in.ReviewerRecorded, in.ReviewedBy != "reviewer",
		in.ReviewerKindNow != in.ReviewerKindReviewed,
		in.ReviewerModelsMeasured() && in.ReviewerModelNow != in.ReviewerModelReviewed:
		// A diff trusts everything outside it to have been read by the same
		// reviewer. A builder's review, or another reviewer's, is not that.
		return "full"
	case !in.IdentityMeasured, in.KindNow != in.KindReviewed, in.AutonomyNow != in.AutonomyReviewed:
		return "full"
	}
	// An edited line counts twice (removed and added), so "changed lines at
	// most the plan's line count" is "at most about half the plan rewritten".
	total := len(lines(in.PlanNow))
	if total == 0 || ChangedLines(in.PlanReviewed, in.PlanNow) > total {
		return "full"
	}
	return "diff"
}

// ChangedLines counts the lines removed from a plus the lines added in b: the
// -/+ lines of a unified diff. It uses the longest common subsequence of
// lines, keeping two rows, so a 2,000-line plan costs a few million
// comparisons and no more memory than two rows of ints.
func ChangedLines(a, b string) int {
	x, y := lines(a), lines(b)
	prev := make([]int, len(y)+1)
	cur := make([]int, len(y)+1)
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			switch {
			case x[i-1] == y[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	common := prev[len(y)]
	return len(x) - common + len(y) - common
}

// lines splits s into lines, ignoring one final newline, so "a\nb" and
// "a\nb\n" are the same two lines.
func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
