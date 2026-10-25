package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

func writeReview(t *testing.T, root, id, plan string) {
	t.Helper()
	dir := filepath.Join(paths.Relay(root), "reviews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"plan":"` + plan + `","verdict":"clean"}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string) string {
	t.Helper()
	if err := os.WriteFile(p, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLatestReviewedPlan(t *testing.T) {
	root := t.TempDir()
	reviews := filepath.Join(paths.Relay(root), "reviews")
	outside := touch(t, filepath.Join(t.TempDir(), "outside-plan.md"))
	inside := touch(t, filepath.Join(root, "inside-plan.md"))

	if got, ok := latestReviewedPlan(reviews); ok {
		t.Fatalf("no reviews yet: want none, got %q", got)
	}

	writeReview(t, root, "20261017-100000-1-1", inside)
	writeReview(t, root, "20261017-231147-49790-17479", outside)
	if got, ok := latestReviewedPlan(reviews); !ok || got != outside {
		t.Fatalf("want the plan of the newest review %q, got %q (ok=%v)", outside, got, ok)
	}

	// A newest review whose plan file is gone must not hide an older plan
	// that still exists.
	writeReview(t, root, "20261018-090000-2-2", filepath.Join(t.TempDir(), "deleted.md"))
	if got, ok := latestReviewedPlan(reviews); !ok || got != outside {
		t.Fatalf("deleted plan must be skipped: want %q, got %q (ok=%v)", outside, got, ok)
	}
}

func TestGateLatestPlanUsage(t *testing.T) {
	if rc := cmdGate([]string{"latest-plan"}); rc != exitcode.Usage {
		t.Fatalf("missing --root: want Usage, got %d", rc)
	}
	// Having no answer is not a failure: the caller falls back to its own rule.
	if rc := cmdGate([]string{"latest-plan", "--root", t.TempDir()}); rc != exitcode.OK {
		t.Fatalf("no reviews: want OK, got %d", rc)
	}
}
