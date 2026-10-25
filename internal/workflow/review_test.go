package workflow

import (
	"errors"
	"strings"
	"testing"
)

func reviewFixture() (ReviewResult, Job) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	h := func(c string) string { return strings.Repeat(c, 64) }
	job := Job{ID: id("1"), AttemptID: id("2"), Kind: "reviewer", PatchHash: h("a"), TreeHash: h("b")}
	return ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: job.AttemptID, PatchHash: job.PatchHash, TreeHash: job.TreeHash,
		Status: ReviewStatusPassed, Findings: []Finding{}}, job
}

func TestValidateReviewAcceptsAMatchingResult(t *testing.T) {
	r, job := reviewFixture()
	if err := ValidateReview(r, job); err != nil {
		t.Fatal(err)
	}
	r.Status = ReviewStatusChangesRequested
	r.Findings = []Finding{{Severity: "high", Path: "a.go", Disposition: "open", Message: "x"}}
	if err := ValidateReview(r, job); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReviewRefusesWhatDoesNotBelongToTheJob(t *testing.T) {
	for name, mutate := range map[string]func(*ReviewResult, *Job){
		"another schema":             func(r *ReviewResult, _ *Job) { r.SchemaVersion = 2 },
		"another job":                func(r *ReviewResult, _ *Job) { r.JobID = strings.Repeat("9", 32) },
		"another attempt":            func(r *ReviewResult, _ *Job) { r.AttemptID = strings.Repeat("9", 32) },
		"another patch":              func(r *ReviewResult, _ *Job) { r.PatchHash = strings.Repeat("9", 64) },
		"another tree":               func(r *ReviewResult, _ *Job) { r.TreeHash = strings.Repeat("9", 64) },
		"an unknown status":          func(r *ReviewResult, _ *Job) { r.Status = "looks fine" },
		"changes with no findings":   func(r *ReviewResult, _ *Job) { r.Status = ReviewStatusChangesRequested },
		"a malformed hash":           func(r *ReviewResult, _ *Job) { r.PatchHash = "abc" },
		"a job that is not a review": func(_ *ReviewResult, j *Job) { j.Kind = "check" },
	} {
		r, job := reviewFixture()
		mutate(&r, &job)
		if err := ValidateReview(r, job); !errors.Is(err, ErrIncompleteEvidence) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
