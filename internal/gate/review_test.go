package gate

import "testing"

// clean returns a ReviewInput that passes every binding, so each test can
// break exactly one thing and name what it broke.
func clean() ReviewInput {
	return ReviewInput{
		Found:            true,
		PlanNow:          "# plan\n",
		PlanReviewed:     "# plan\n",
		PlanMeasured:     true,
		KindNow:          "codex",
		KindReviewed:     "codex",
		AutonomyNow:      "auto",
		AutonomyReviewed: "auto",
		IdentityMeasured: true,

		ReviewerRecorded:              true,
		ReviewerKindNow:               "claude",
		ReviewerKindReviewed:          "claude",
		ReviewerModelNow:              "opus",
		ReviewerModelReviewed:         "opus",
		ReviewerModelMeasuredNow:      true,
		ReviewerModelMeasuredReviewed: true,
		ReviewedBy:                    "reviewer",

		Verdict: "clean",
	}
}

func TestReview(t *testing.T) {
	objected := func(m func(*ReviewInput)) ReviewInput {
		in := clean()
		m(&in)
		return in
	}

	cases := []struct {
		name   string
		in     ReviewInput
		code   int
		detail string
	}{
		{"a clean current review allows the handoff", clean(), 0, ""},
		{"no review at all", objected(func(i *ReviewInput) { i.Found = false }), 24, "absent"},
		{"the plan was edited", objected(func(i *ReviewInput) { i.PlanNow = "# other\n" }), 24, "plan"},
		{"no reviewer is recorded", objected(func(i *ReviewInput) { i.ReviewerRecorded = false }), 24, "no_reviewer"},
		{"the review was written by the builder", objected(func(i *ReviewInput) { i.ReviewedBy = "builder" }), 24, "builder_review"},
		{"a pre-upgrade review has no author", objected(func(i *ReviewInput) { i.ReviewedBy = "" }), 24, "builder_review"},
		{"the reviewer kind changed", objected(func(i *ReviewInput) { i.ReviewerKindNow = "codex" }), 24, "reviewer"},
		{"the reviewer model changed", objected(func(i *ReviewInput) { i.ReviewerModelNow = "sonnet" }), 24, "reviewer"},
		{"an unmeasured model with the same kind passes", objected(func(i *ReviewInput) {
			i.ReviewerModelNow, i.ReviewerModelReviewed, i.ReviewerModelMeasuredReviewed = "sonnet", "", false
		}), 0, ""},
		{"an unmeasured model with another kind refuses", objected(func(i *ReviewInput) {
			i.ReviewerKindNow, i.ReviewerModelMeasuredNow = "codex", false
		}), 24, "reviewer"},
		{"no reviewer outranks a builder review", objected(func(i *ReviewInput) {
			i.ReviewerRecorded, i.ReviewedBy = false, "builder"
		}), 24, "no_reviewer"},
		{"a builder review outranks a builder change", objected(func(i *ReviewInput) {
			i.ReviewedBy, i.KindNow = "builder", "claude"
		}), 24, "builder_review"},
		{"no reviewer outranks a plan edit", objected(func(i *ReviewInput) {
			i.PlanNow, i.ReviewerRecorded = "# other\n", false
		}), 24, "no_reviewer"},
		{"no reviewer and no review is no_reviewer, not absent", objected(func(i *ReviewInput) {
			i.Found, i.ReviewerRecorded = false, false
		}), 24, "no_reviewer"},
		{"the builder kind changed", objected(func(i *ReviewInput) { i.KindNow = "claude" }), 24, "builder"},
		{"the builder autonomy changed", objected(func(i *ReviewInput) { i.AutonomyNow = "ask" }), 24, "builder"},
		{"objections stand", objected(func(i *ReviewInput) { i.Verdict = "objections" }), 24, "objections"},
		{"objections overruled with a reason", objected(func(i *ReviewInput) {
			i.Verdict = "objections"
			i.OverrideReason = "I commit here, not you"
		}), 0, ""},

		// Fail-closed cases. Each of these reads as "unchanged" if the code
		// compares strings without consulting the measured flag.
		{"the plan could not be read", objected(func(i *ReviewInput) {
			i.PlanNow, i.PlanReviewed, i.PlanMeasured = "", "", false
		}), 24, "plan"},
		{"the identity could not be resolved", objected(func(i *ReviewInput) {
			i.KindNow, i.KindReviewed = "", ""
			i.AutonomyNow, i.AutonomyReviewed = "", ""
			i.IdentityMeasured = false
		}), 24, "builder"},
		{"an unrecognised verdict is not approval", objected(func(i *ReviewInput) {
			i.Verdict = ""
		}), 24, "objections"},
		{"a blank override is not an override", objected(func(i *ReviewInput) {
			i.Verdict = "objections"
			i.OverrideReason = "   \n\t "
		}), 24, "objections"},

		{"an expected review that is no longer newest", objected(func(i *ReviewInput) {
			i.ExpectedID, i.NewestID = "20260910-000001-1-1", "20260910-000002-1-1"
		}), 24, "superseded"},
		{"an expected review that is still newest", objected(func(i *ReviewInput) {
			i.ExpectedID, i.NewestID = "20260910-000002-1-1", "20260910-000002-1-1"
		}), 0, ""},
		{"no expectation is not a supersession", objected(func(i *ReviewInput) {
			i.ExpectedID, i.NewestID = "", "20260910-000002-1-1"
		}), 0, ""},
	}

	for _, c := range cases {
		got := Review(c.in)
		if got.Code != c.code {
			t.Errorf("%s: code = %d, want %d", c.name, got.Code, c.code)
			continue
		}
		if c.detail == "" {
			continue
		}
		if len(got.Detail) == 0 || got.Detail[0] != c.detail {
			t.Errorf("%s: detail = %v, want first item %q", c.name, got.Detail, c.detail)
		}
	}
}

// A plan edit and a builder change at once names the plan first.
func TestReviewNamesThePlanEditFirst(t *testing.T) {
	in := clean()
	in.PlanNow = "# edited\n"
	in.KindNow = "claude"
	if got := Review(in); got.Detail[0] != "plan" {
		t.Errorf("detail = %v, want first item \"plan\"", got.Detail)
	}
}

// A reviewer or builder refusal names the side the review was made with and
// the side now, so the script that explains it reads neither record.
func TestReviewNamesWhatChanged(t *testing.T) {
	for _, c := range []struct {
		name string
		m    func(*ReviewInput)
		want []string
	}{
		{"the reviewer model", func(i *ReviewInput) { i.ReviewerModelNow = "sonnet" }, []string{"reviewer", "claude/opus", "claude/sonnet"}},
		{"the reviewer kind, unmeasured now", func(i *ReviewInput) {
			i.ReviewerKindNow, i.ReviewerModelNow, i.ReviewerModelMeasuredNow = "codex", "", false
		}, []string{"reviewer", "claude/opus", "codex/unmeasured"}},
		{"a measured but empty model", func(i *ReviewInput) { i.ReviewerKindNow, i.ReviewerModelNow = "codex", "" }, []string{"reviewer", "claude/opus", "codex/?"}},
		{"the builder autonomy", func(i *ReviewInput) { i.AutonomyNow = "full" }, []string{"builder", "codex/auto", "codex/full"}},
		{"an unreadable builder record", func(i *ReviewInput) {
			i.KindNow, i.AutonomyNow, i.IdentityMeasured = "", "", false
		}, []string{"builder", "codex/auto", "?/?"}},
	} {
		in := clean()
		c.m(&in)
		got := Review(in)
		if got.Code != 24 || len(got.Detail) != 3 || got.Detail[0] != c.want[0] || got.Detail[1] != c.want[1] || got.Detail[2] != c.want[2] {
			t.Errorf("%s: %+v, want 24 %v", c.name, got, c.want)
		}
	}
}
