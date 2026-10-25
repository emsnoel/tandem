package triage

import (
	"fmt"
	"strings"
	"testing"
)

var (
	yes, no = true, false
	cohort  = Cohort{Schema: EvidenceSchema, PolicyHash: "p", JudgeConfigHash: "j", ReviewerConfigHash: "r"}
)

// pairRow is a complete, matched observation under the cohort. Tests change
// what they are about.
func pairRow(mut ...func(*Row)) Row {
	r := Row{JobID: "job", Verdict: "clean", WouldSkip: true, Reviewed: &yes, ReviewChangedTree: &no, JudgedTree: "t", ReviewedTree: "t",
		Identity: EvidenceIdentity{Schema: EvidenceSchema, PolicyHash: "p", JudgeConfigHash: "j", ReviewerConfigHash: "r", InputHash: "in", Complete: true}}
	for _, m := range mut {
		m(&r)
	}
	return r
}

func many(n int, mut ...func(*Row)) []Row {
	var out []Row
	for i := 0; i < n; i++ {
		r := pairRow(mut...)
		r.JobID = fmt.Sprintf("job-%d", i)
		out = append(out, r)
	}
	return out
}

func TestSummarizeCountsPairsAndMisses(t *testing.T) {
	rows := []Row{
		pairRow(),
		pairRow(func(r *Row) { r.ReviewChangedTree = &yes }),                      // would skip, reviewer found something: a miss
		pairRow(func(r *Row) { r.WouldSkip = false; r.ReviewChangedTree = &yes }), // would review, and rightly
		pairRow(func(r *Row) { r.WouldSkip = false; r.ReviewChangedTree = &no }),  // would review, nothing found
	}
	r := Summarize(rows, cohort, 0)
	if r.Pairs != 4 || r.SkipPairs != 2 || r.ReviewerChanged != 2 || r.Misses != 1 || r.Excluded != 0 || !r.Healthy {
		t.Fatalf("%+v", r)
	}
	want := map[string]int{"true_negative": 1, "false_negative": 1, "true_positive": 1, "false_positive": 1}
	for k, v := range want {
		if r.Classes[k] != v {
			t.Errorf("%s = %d, want %d (%v)", k, r.Classes[k], v, r.Classes)
		}
	}
}

func TestSummarizeSaysWhyEachRowWasLeftOut(t *testing.T) {
	rows := []Row{
		pairRow(), // counted
		pairRow(func(r *Row) { r.Identity = EvidenceIdentity{} }), // no evidence identity
		pairRow(func(r *Row) { r.Verdict = "unavailable" }),
		pairRow(func(r *Row) { r.Reviewed, r.ReviewChangedTree = nil, nil }), // judged, no review outcome yet
		pairRow(func(r *Row) { r.Reviewed = &no }),                           // no reviewer ran
		pairRow(func(r *Row) { r.Identity.Complete = false }),
		pairRow(func(r *Row) { r.JudgedTree = "" }),
		pairRow(func(r *Row) { r.ReviewedTree = "other" }),
		pairRow(func(r *Row) { r.Truncated = true }),
		pairRow(func(r *Row) { r.Identity.PolicyHash = "old" }),
		pairRow(func(r *Row) { r.Identity.JudgeConfigHash = "old" }),
		pairRow(func(r *Row) { r.Identity.ReviewerConfigHash = "old" }),
	}
	r := Summarize(rows, cohort, 0)
	want := map[string]int{"legacy_record": 1, "judge_unavailable": 1, "awaiting_review": 1, "not_reviewed": 1, "incomplete_identity": 1,
		"tree_unknown": 1, "tree_mismatch": 1, "truncated_input": 1, "other_cohort": 3}
	for k, v := range want {
		if r.ExcludedReasons[k] != v {
			t.Errorf("%s = %d, want %d (%v)", k, r.ExcludedReasons[k], v, r.ExcludedReasons)
		}
	}
	if r.Pairs != 1 || r.Excluded != 11 || r.MissingPairs != 1 {
		t.Errorf("%+v", r)
	}
	total := 0
	for _, v := range r.ExcludedReasons {
		total += v
	}
	if total != r.Excluded {
		t.Errorf("the reasons add up to %d, not %d", total, r.Excluded)
	}
	if got := Summarize(nil, cohort, 0); got.Pairs != 0 || !got.Healthy || got.Passes() {
		t.Errorf("an empty report: %+v", got)
	}
}

func TestPassesNeedsThirtyPairsTenChangesNoMissesAndHealth(t *testing.T) {
	for name, c := range map[string]struct {
		r    Report
		want bool
	}{
		"enough":          {Report{SkipPairs: 30, ReviewerChanged: 10, Healthy: true}, true},
		"too few skips":   {Report{SkipPairs: 29, ReviewerChanged: 10, Healthy: true}, false},
		"no skips":        {Report{ReviewerChanged: 20, Healthy: true}, false},
		"too few changes": {Report{SkipPairs: 30, ReviewerChanged: 9, Healthy: true}, false},
		"a miss":          {Report{SkipPairs: 60, ReviewerChanged: 20, Misses: 1, Healthy: true}, false},
		"unhealthy":       {Report{SkipPairs: 60, ReviewerChanged: 20, Healthy: false}, false},
		"a zero value":    {Report{}, false},
	} {
		if got := c.r.Passes(); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestCalibrationCorruptRowClosesGate(t *testing.T) {
	// A cohort that would otherwise be open.
	rows := many(30, func(r *Row) { r.ReviewChangedTree = &no })
	rows = append(rows, many(10, func(r *Row) { r.WouldSkip = false; r.ReviewChangedTree = &yes })...)
	for i := range rows {
		rows[i].JobID = fmt.Sprintf("j%d", i)
	}
	open := Summarize(rows, cohort, 0)
	if !open.Passes() || !open.Healthy {
		t.Fatalf("fixture: %+v", open)
	}
	// One row that cannot be read.
	r := Summarize(rows, cohort, 1)
	if r.Healthy || r.Passes() || r.Corrupt != 1 {
		t.Errorf("a corrupt row must close the gate, not disappear: %+v", r)
	}
	// And the counts are unchanged: health is separate from the sample.
	if r.Pairs != open.Pairs || r.SkipPairs != open.SkipPairs {
		t.Errorf("%+v vs %+v", r, open)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "corrupt") {
		t.Errorf("%v", r.Notes)
	}
}

func TestCalibrationConfigChangeSeparatesCohorts(t *testing.T) {
	old := many(40)
	// A new reviewer prompt or model starts a new cohort: the old pairs say
	// nothing about it, and are shown as excluded, not pooled.
	next := cohort
	next.ReviewerConfigHash = "r2"
	r := Summarize(old, next, 0)
	if r.Pairs != 0 || r.SkipPairs != 0 || r.ExcludedReasons["other_cohort"] != 40 {
		t.Errorf("%+v", r)
	}
	// Likewise a new judge configuration, and a new policy.
	for name, mut := range map[string]func(*Cohort){
		"judge":  func(c *Cohort) { c.JudgeConfigHash = "j2" },
		"policy": func(c *Cohort) { c.PolicyHash = "p2" },
		"schema": func(c *Cohort) { c.Schema = "triage-evidence/v2" },
	} {
		c := cohort
		mut(&c)
		if r := Summarize(old, c, 0); r.Pairs != 0 {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// New rows under the new cohort count, on their own.
	mixed := append(many(40), many(5, func(r *Row) { r.Identity.ReviewerConfigHash = "r2" })...)
	if r := Summarize(mixed, next, 0); r.Pairs != 5 || r.Excluded != 40 {
		t.Errorf("%+v", r)
	}
}

func TestAuditMissClosesCohort(t *testing.T) {
	rows := many(60, func(r *Row) { r.ReviewChangedTree = &yes })
	rows = append(rows, many(40, func(r *Row) { r.WouldSkip = false })...)
	good := Summarize(rows, cohort, 0)
	// All 100 would-skip/would-review mix is fine until a reviewer finds
	// something in a task the judge would have skipped.
	for i := range rows[:60] {
		rows[i].ReviewChangedTree = &no
	}
	rows[0].ReviewChangedTree = &yes
	rows[0].WouldSkip = true
	r := Summarize(rows, cohort, 0)
	if r.Misses != 1 || !r.Closed || r.Passes() || r.Classes["false_negative"] != 1 {
		t.Errorf("a real issue found after a would-skip must close the cohort: %+v", r)
	}
	// It stays closed however many clean rows follow.
	for i := 0; i < 200; i++ {
		rows = append(rows, pairRow(func(x *Row) { x.JobID = fmt.Sprintf("later-%d", i) }))
	}
	if after := Summarize(rows, cohort, 0); after.Passes() || !after.Closed {
		t.Errorf("later clean rows reopened it: %+v", after)
	}
	_ = good
}

func TestAuditCountsTheSelectedPairsSeparately(t *testing.T) {
	rows := []Row{pairRow(func(r *Row) { r.AuditSelected = true }), pairRow(), pairRow(func(r *Row) { r.AuditSelected = true; r.ReviewChangedTree = &yes })}
	r := Summarize(rows, cohort, 0)
	if r.AuditPairs != 2 || r.AuditMisses != 1 {
		t.Errorf("%+v", r)
	}
}

func TestReportStatesItsUncertainty(t *testing.T) {
	r := Summarize(many(30), cohort, 0)
	if r.MissRateUpper95 < 0.09 || r.MissRateUpper95 > 0.11 {
		t.Errorf("zero misses in 30 allows a true rate near 10%%: %v", r.MissRateUpper95)
	}
	text := strings.Join(r.Notes, " ")
	for _, want := range []string{"not a proof", "not been run against"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notes must not imply proven safety; missing %q in %q", want, text)
		}
	}
	if r2 := Summarize(many(300), cohort, 0); r2.MissRateUpper95 >= r.MissRateUpper95 {
		t.Errorf("more evidence narrows the bound: %v then %v", r.MissRateUpper95, r2.MissRateUpper95)
	}
	if r0 := Summarize(nil, cohort, 0); r0.MissRateUpper95 != 1 {
		t.Errorf("no evidence bounds nothing: %v", r0.MissRateUpper95)
	}
	// Thirty would-skip pairs alone do not open the gate: it also needs the
	// reviewer to have found something ten times, or it has never been tested.
	if r.Passes() {
		t.Errorf("%+v", r)
	}
}
