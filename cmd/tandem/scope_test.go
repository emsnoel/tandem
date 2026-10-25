package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/gate"
	"github.com/emsnoel/tandem/internal/paths"
)

// scopeRoot writes a repo with one plan and, when reviewed is non-empty, one
// review of it made against that text by a codex/auto builder's reviewer
// (claude/opus, measured).
func scopeRoot(t *testing.T, now, reviewed string) (root, plan string) {
	t.Helper()
	root = t.TempDir()
	plan = filepath.Join(root, "plan.md")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(plan, []byte(now), 0o644))
	rv := filepath.Join(paths.Relay(root), "reviews")
	must(os.MkdirAll(rv, 0o755))
	must(os.WriteFile(filepath.Join(paths.Relay(root), "builder.json"), []byte(codexBuilder), 0o644))
	must(os.WriteFile(filepath.Join(paths.Relay(root), "reviewer.json"),
		[]byte(`{"kind":"claude","name":"reviewer","model":"opus","model_measured":true}`), 0o644))
	if reviewed != "" {
		id := "20260926-170048-111-222"
		must(os.WriteFile(filepath.Join(rv, id+".json"),
			[]byte(`{"plan":"`+plan+`","verdict":"objections","kind":"codex","autonomy":"auto",`+
				`"reviewed_by":"reviewer","reviewer_kind":"claude","reviewer_model":"opus","reviewer_model_measured":true}`), 0o644))
		must(os.WriteFile(filepath.Join(rv, id+".plan.md"), []byte(reviewed), 0o644))
	}
	return root, plan
}

// reviewInputFor is what both `gate review` and `gate scope` judge, so this
// pins that it reads the snapshot, the plan and the identity scope needs.
func TestReviewInputForFeedsScope(t *testing.T) {
	root, plan := scopeRoot(t, "a\nb changed\nc\nd\n", "a\nb\nc\nd\n")
	in, id := reviewInputFor(plan, root)
	if got := gate.ReviewScope(in); got != "diff" || id != "20260926-170048-111-222" {
		t.Fatalf("got %q, id %q; want diff, 20260926-170048-111-222", got, id)
	}

	root, plan = scopeRoot(t, "a\n", "")
	in, _ = reviewInputFor(plan, root)
	if got := gate.ReviewScope(in); got != "full" {
		t.Fatalf("no review: got %q, want full", got)
	}
}

// A review the builder wrote (or one from before reviewed_by existed) is never
// the baseline for a diff: the reviewer has not read the unchanged parts.
func TestReviewInputForBuilderReviewScopesFull(t *testing.T) {
	root, plan := scopeRoot(t, "a\nb changed\nc\nd\n", "a\nb\nc\nd\n")
	meta := filepath.Join(paths.Relay(root), "reviews", "20260926-170048-111-222.json")
	if err := os.WriteFile(meta, []byte(`{"plan":"`+plan+`","verdict":"objections","kind":"codex","autonomy":"auto"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := reviewInputFor(plan, root)
	if got := gate.ReviewScope(in); got != "full" {
		t.Fatalf("pre-upgrade review: got %q, want full", got)
	}
}
