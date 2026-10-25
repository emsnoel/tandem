package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/build"
)

// reviewRound runs a judged task's first handback, so task 1 is in review with
// its baseline tree recorded.
func reviewRound(t *testing.T, tree map[string]string) (root, dir string, f *fakeAdvance) {
	t.Helper()
	root, dir = buildRepo(t, "check", 0)
	judgeOn(t, dir)
	// vendor/dep.go was already dirty before the task started.
	b := load(t, dir)
	b.Tasks[0].TreeBefore = map[string]string{"vendor/dep.go": "d1"}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f = &fakeAdvance{
		statuses: []string{"idle"}, clock: time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC),
		judgeVerdict: Verdict{RecordID: "r1"}, usage: &build.Usage{},
		dirties: []map[string]string{tree},
	}
	if got := advance(t, f, root, append(reviewArgs("e1", "2026-09-29T12:00:00Z"), "--report", "/r/first.md")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q", got)
	}
	f.clock = f.clock.Add(time.Minute)
	return root, dir, f
}

// secondHandback is the reviewer pane's clean verdict on task 1.
func secondHandback(t *testing.T, f *fakeAdvance, root string) string {
	t.Helper()
	return advance(t, f, root, append(reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean"), "--report", "/r/review.md")...)
}

// builderReview is task 1's review handed back from the builder's pane.
func builderReview(t *testing.T, f *fakeAdvance, root, id, created string) string {
	t.Helper()
	return advance(t, f, root, append(reviewArgs(id, created), "--review", "--verdict", "clean", "--from", "builder", "--report", "/r/review.md")...)
}

// A reviewer that fixes a file which was already dirty and not part of the task
// still changes what the earlier check ran against.
func TestReviewChangedExistingDirtyDependencyRerunsCheck(t *testing.T) {
	root, dir, f := reviewRound(t, map[string]string{"src/greet.sh": "h1", "vendor/dep.go": "d1"})
	f.dirties = []map[string]string{{"src/greet.sh": "h1", "vendor/dep.go": "d2"}}
	f.dirtyN = 0
	before := len(f.checked)
	if got := secondHandback(t, f, root); got != "next 2" {
		t.Fatalf("printed %q", got)
	}
	if len(f.checked) != before+1 {
		t.Fatalf("checks %d, want %d: the old check no longer describes this tree", len(f.checked), before+1)
	}
	r := load(t, dir).Tasks[0].Review
	if r.Changed == nil || *r.Changed || len(r.OtherPaths) != 1 || r.OtherPaths[0] != "vendor/dep.go" {
		t.Fatalf("attribution must stay separate from invalidation: %+v", r)
	}
}

func TestReviewThatChangedNothingKeepsTheCheck(t *testing.T) {
	root, _, f := reviewRound(t, map[string]string{"src/greet.sh": "h1"})
	f.dirties, f.dirtyN = []map[string]string{{"src/greet.sh": "h1"}}, 0
	before := len(f.checked)
	if got := secondHandback(t, f, root); got != "next 2" || len(f.checked) != before {
		t.Fatalf("printed %q, checks %d -> %d", got, before, len(f.checked))
	}
}

// A review handed back from the builder's pane is not a review: the round
// stays open, nothing is accepted, and the evidence so far is kept.
func TestABuildersReviewLeavesTheTaskUnaccepted(t *testing.T) {
	root, dir, f := reviewRound(t, map[string]string{"src/greet.sh": "h1"})
	f.dirties, f.dirtyN = []map[string]string{{"src/greet.sh": "h1"}}, 0
	if got := builderReview(t, f, root, "e2", "2026-09-29T12:01:00Z"); !strings.HasPrefix(got, "refused ") {
		t.Fatalf("printed %q", got)
	}
	b := load(t, dir)
	task := b.Tasks[0]
	if task.Status != build.StatusReviewing || task.DoneAt != "" || b.Current != 1 || b.State != build.StateRunning {
		t.Fatalf("status %s done_at %q current %d state %s", task.Status, task.DoneAt, b.Current, b.State)
	}
	if task.Report != "/r/first.md" || task.ReviewReport != "" || task.CheckExit == nil || *task.CheckExit != 0 {
		t.Fatalf("evidence was not retained, or a refused review was taken: %+v", task)
	}
	if task.Review.Reviewed != nil {
		t.Fatalf("review %+v; a refused handback must record nothing", task.Review)
	}
}

func TestHandingBackAgainStillNeedsTheReviewer(t *testing.T) {
	root, dir, f := reviewRound(t, map[string]string{"src/greet.sh": "h1"})
	f.dirties, f.dirtyN = []map[string]string{{"src/greet.sh": "h1"}}, 0
	builderReview(t, f, root, "e2", "2026-09-29T12:01:00Z")
	if got := advance(t, f, root, append(reviewArgs("e3", "2026-09-29T12:02:00Z"), "--report", "/r/again.md")...); !strings.HasPrefix(got, "ignored ") {
		t.Fatalf("a builder's repeat closed the round: %q", got)
	}
	if load(t, dir).Tasks[0].Status == build.StatusDone {
		t.Fatal("accepted without a review")
	}
	f.clock = f.clock.Add(time.Minute)
	if got := advance(t, f, root, append(reviewerArgs("e4", "2026-09-29T12:03:00Z", "clean"), "--report", "/r/review.md")...); got != "next 2" {
		t.Fatalf("the reviewer's verdict: %q", got)
	}
}

// reviewerEdited ends task 1's round with a reviewer that changed a task file.
func reviewerEdited(t *testing.T) (root, dir string) {
	t.Helper()
	root, dir, f := reviewRound(t, map[string]string{"src/greet.sh": "h1"})
	f.dirties, f.dirtyN = []map[string]string{{"src/greet.sh": "h2"}}, 0
	if got := secondHandback(t, f, root); !strings.HasPrefix(got, "stopped review_edited_tree ") {
		t.Fatalf("printed %q", got)
	}
	return root, dir
}

func TestAnInvalidReviewCanBeExplicitlyAccepted(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "copilot", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			root, dir := reviewerEdited(t)
			b := load(t, dir)
			b.Builder = kind
			if err := b.Save(dir); err != nil {
				t.Fatal(err)
			}
			if rc := cmdBuildAccept([]string{"--root", root, "--task", "1", "--because", "read the diff and the review report myself"}); rc != 0 {
				t.Fatalf("accept rc %d", rc)
			}
			task := load(t, dir).Tasks[0]
			if task.Status != build.StatusDone || task.Accepted == "" {
				t.Fatalf("task %+v", task)
			}
			if task.Review.Complete() {
				t.Fatalf("acceptance fabricated a valid review: %+v", task.Review)
			}
		})
	}
}

func TestAnInvalidReviewNeedsANonBlankReasonToAccept(t *testing.T) {
	root, _ := reviewerEdited(t)
	devnull, _ := os.Open(os.DevNull)
	old := os.Stderr
	os.Stderr = devnull
	defer func() { os.Stderr = old }()
	if rc := cmdBuildAccept([]string{"--root", root, "--task", "1", "--because", "  "}); rc == 0 {
		t.Fatal("accepted with a blank reason")
	}
}
