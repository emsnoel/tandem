package workflow

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func ip(n int) *int { return &n }

func finding(mut ...func(*Finding)) Finding {
	f := Finding{Severity: "medium", Path: "internal/state/events.go", Disposition: "open", Message: "State changes before the effect is persisted.", StartLine: ip(42), EndLine: ip(45)}
	for _, m := range mut {
		m(&f)
	}
	return f
}

func TestReviewPassedEmptyFindingsIsValid(t *testing.T) {
	r, job := reviewFixture()
	if err := ValidateReview(r, job); err != nil {
		t.Fatalf("a passed review with no findings, bound to its job: %v", err)
	}
}

func TestReviewPassedWithOpenHighFindingRejected(t *testing.T) {
	for _, sev := range []string{"critical", "high"} {
		r, job := reviewFixture()
		r.Findings = []Finding{finding(func(f *Finding) { f.Severity = sev })}
		err := ValidateReview(r, job)
		if err == nil || !errors.Is(err, ErrIncompleteEvidence) || !strings.Contains(err.Error(), "unresolved") {
			t.Errorf("passed with an open %s finding: %v", sev, err)
		}
	}
	// A blocking finding that is rejected needs a planner's reason, which a
	// reviewer's own result cannot supply, so it does not make a pass either.
	r, job := reviewFixture()
	r.Findings = []Finding{finding(func(f *Finding) { f.Severity = "high"; f.Disposition = "rejected" })}
	if err := ValidateReview(r, job); err == nil || !strings.Contains(err.Error(), "planner") {
		t.Errorf("a rejected blocking finding in a pass: %v", err)
	}
	// What does not block may stay open, be fixed or be rejected.
	for _, c := range []struct{ sev, disp string }{{"medium", "open"}, {"low", "open"}, {"info", "open"}, {"high", "fixed"}, {"critical", "fixed"}, {"medium", "rejected"}} {
		r, job := reviewFixture()
		r.Findings = []Finding{finding(func(f *Finding) { f.Severity = c.sev; f.Disposition = c.disp })}
		if err := ValidateReview(r, job); err != nil {
			t.Errorf("%s/%s in a pass: %v", c.sev, c.disp, err)
		}
	}
	// A request for changes may carry any of them; it is never accepted anyway.
	r, job = reviewFixture()
	r.Status = ReviewStatusChangesRequested
	r.Findings = []Finding{finding(func(f *Finding) { f.Severity = "high" })}
	if err := ValidateReview(r, job); err != nil {
		t.Errorf("changes_requested with an open high finding: %v", err)
	}
}

func TestValidateReviewFindingSchema(t *testing.T) {
	long := strings.Repeat("a", maxFindingMessage+1)
	for name, c := range map[string]struct {
		f    Finding
		want string
	}{
		"unknown severity":    {finding(func(f *Finding) { f.Severity = "urgent" }), "severity"},
		"empty severity":      {finding(func(f *Finding) { f.Severity = "" }), "severity"},
		"unknown disposition": {finding(func(f *Finding) { f.Disposition = "ignored" }), "disposition"},
		"empty path":          {finding(func(f *Finding) { f.Path = "" }), "path"},
		"absolute path":       {finding(func(f *Finding) { f.Path = "/etc/passwd" }), "escapes"},
		"parent path":         {finding(func(f *Finding) { f.Path = "../outside" }), "escapes"},
		"nested parent path":  {finding(func(f *Finding) { f.Path = "a/../../b" }), "escapes"},
		"backslash path":      {finding(func(f *Finding) { f.Path = `a\b` }), "escapes"},
		"control in path":     {finding(func(f *Finding) { f.Path = "a\nb" }), "control"},
		"zero start line":     {finding(func(f *Finding) { f.StartLine = ip(0) }), "line"},
		"negative start line": {finding(func(f *Finding) { f.StartLine = ip(-3) }), "line"},
		"zero end line":       {finding(func(f *Finding) { f.EndLine = ip(0) }), "line"},
		"end before start":    {finding(func(f *Finding) { f.StartLine, f.EndLine = ip(10), ip(9) }), "before"},
		"end without start":   {finding(func(f *Finding) { f.StartLine, f.EndLine = nil, ip(9) }), "start_line"},
		"empty message":       {finding(func(f *Finding) { f.Message = "  \n" }), "message"},
		"message too long":    {finding(func(f *Finding) { f.Message = long }), "message"},
		"message with a NUL":  {finding(func(f *Finding) { f.Message = "a\x00b" }), "message"},
		"message not UTF-8":   {finding(func(f *Finding) { f.Message = "a\xffb" }), "message"},
		"path not UTF-8":      {finding(func(f *Finding) { f.Path = "a\xffb" }), "path"},
	} {
		r, job := reviewFixture()
		r.Status = ReviewStatusChangesRequested
		r.Findings = []Finding{c.f}
		err := ValidateReview(r, job)
		if err == nil || !errors.Is(err, ErrIncompleteEvidence) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
	// The same checks apply whatever the status, and to every finding.
	r, job := reviewFixture()
	r.Status = ReviewStatusFailed
	r.Findings = []Finding{finding(), finding(func(f *Finding) { f.Path = "../x" })}
	if err := ValidateReview(r, job); err == nil || !strings.Contains(err.Error(), "finding 2") {
		t.Errorf("the second finding was not checked: %v", err)
	}
	// Whole-file and single-line locations are fine.
	r, job = reviewFixture()
	r.Status = ReviewStatusChangesRequested
	r.Findings = []Finding{finding(func(f *Finding) { f.StartLine, f.EndLine = nil, nil }), finding(func(f *Finding) { f.EndLine = nil }), finding(func(f *Finding) { f.Path = "." })}
	if err := ValidateReview(r, job); err != nil {
		t.Errorf("%v", err)
	}
	if !utf8.ValidString("日本語") || len(long) <= maxFindingMessage {
		t.Fatal("fixture")
	}
	// A reviewer cannot bury the planner in findings.
	r, job = reviewFixture()
	r.Status = ReviewStatusChangesRequested
	for i := 0; i <= maxFindings; i++ {
		r.Findings = append(r.Findings, finding())
	}
	if err := ValidateReview(r, job); err == nil || !strings.Contains(err.Error(), "findings") {
		t.Errorf("%d findings: %v", len(r.Findings), err)
	}
}

func TestBlockingSeverities(t *testing.T) {
	for sev, want := range map[string]bool{"critical": true, "high": true, "medium": false, "low": false, "info": false, "": false} {
		if got := Blocking(sev); got != want {
			t.Errorf("%q: %v", sev, got)
		}
	}
}
