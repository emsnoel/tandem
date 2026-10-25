package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
)

// pausedReviewRepo is a judged build whose task 1 handed back over budget, so
// its review is paused. The plan file matches the build's recorded hash, so
// `build start` resumes it.
func pausedReviewRepo(t *testing.T) (root, dir string, f *fakeAdvance) {
	t.Helper()
	root, dir = buildRepo(t, "check", 100)
	judgeOn(t, dir)
	raw := []byte(threeTaskPlan)
	if err := os.WriteFile(filepath.Join(root, "p.md"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	b := load(t, dir)
	b.PlanSHA = hex.EncodeToString(sum[:])
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f = &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{Input: 101}}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "stopped budget ") {
		t.Fatalf("printed %q", got)
	}
	return root, dir, f
}

// runStart runs `build start` for the fixture's plan and returns its JSON.
func runStart(t *testing.T, root string, budget string, extra ...string) (map[string]any, int) {
	t.Helper()
	r, w, _ := os.Pipe()
	old, oldErr := os.Stdout, os.Stderr
	os.Stdout = w
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull
	args := append([]string{"--plan", filepath.Join(root, "p.md"), "--root", root, "--check", "check", "--budget", budget, "--judge"}, extra...)
	rc := cmdBuildStart(args)
	w.Close()
	os.Stdout, os.Stderr = old, oldErr
	out, _ := io.ReadAll(r)
	var got map[string]any
	if rc == 0 {
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("start printed %q: %v", out, err)
		}
	}
	return got, rc
}

func TestAdvanceBudgetPausesReviewWithoutAcceptingTheTask(t *testing.T) {
	_, dir, f := pausedReviewRepo(t)
	b := load(t, dir)
	task := b.Tasks[0]
	if len(f.judged) != 0 {
		t.Error("asked Jev while over budget")
	}
	if task.Status != build.StatusReviewing || task.DoneAt != "" || b.Current != 1 {
		t.Errorf("status %s done_at %q current %d; the task must stay unaccepted", task.Status, task.DoneAt, b.Current)
	}
	if task.Review == nil || !task.Review.Pending || task.Review.Delivered {
		t.Errorf("review %+v, want pending and undelivered", task.Review)
	}
	if b.State != build.StateStopped || b.StopReason != "budget" || b.StopTask != 1 {
		t.Errorf("state %s reason %s task %d", b.State, b.StopReason, b.StopTask)
	}
}

func TestFinalTaskBudgetDoesNotBypassReview(t *testing.T) {
	root, dir := buildRepo(t, "check", 100)
	judgeOn(t, dir)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status = build.StatusDone, build.StatusDone
	b.Tasks[2].Status, b.Tasks[2].TreeBefore, b.Current = build.StatusSent, map[string]string{}, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{Input: 500}}
	if got := advance(t, f, root, "--task", "3", "--event", "e3"); !strings.HasPrefix(got, "stopped budget ") {
		t.Fatalf("printed %q", got)
	}
	b = load(t, dir)
	if b.State == build.StateFinished || b.Tasks[2].Status == build.StatusDone || len(f.judged) != 0 {
		t.Fatalf("an unreviewed final task slipped through: state %s status %s", b.State, b.Tasks[2].Status)
	}
}

func TestLegacyNoJudgeStillCompletes(t *testing.T) {
	root, dir := buildRepo(t, "check", 100)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status = build.StatusDone, build.StatusDone
	b.Tasks[2].Status, b.Tasks[2].TreeBefore, b.Current = build.StatusSent, map[string]string{}, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{Input: 50}}
	if got := handBackReviewed(t, f, root, 3); !strings.HasPrefix(got, "finished ") {
		t.Fatalf("printed %q", got)
	}
	b = load(t, dir)
	if t3 := b.Tasks[2]; t3.Status != build.StatusDone || t3.Review == nil || t3.Review.Result != "clean" || len(f.judged) != 0 {
		t.Fatalf("a build without a judge must finish through the reviewer pane, never Jev: %+v", t3)
	}
}

// Without a judge, a final task over its budget is held for review exactly as
// with one: spending never accepts an unreviewed task.
func TestLegacyNoJudgeHoldsAnOverBudgetFinalTaskForReview(t *testing.T) {
	root, dir := buildRepo(t, "check", 100)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status = build.StatusDone, build.StatusDone
	b.Tasks[2].Status, b.Tasks[2].TreeBefore, b.Current = build.StatusSent, map[string]string{}, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{Input: 500}}
	if got := advance(t, f, root, "--task", "3"); !strings.HasPrefix(got, "stopped budget ") {
		t.Fatalf("printed %q", got)
	}
	b = load(t, dir)
	if b.State == build.StateFinished || b.Tasks[2].Status == build.StatusDone || len(f.judged) != 0 {
		t.Fatalf("an unreviewed final task slipped through: state %s status %s", b.State, b.Tasks[2].Status)
	}
}

func TestAdvanceIgnoresARepeatedHandbackWhileTheReviewIsPaused(t *testing.T) {
	root, dir, f := pausedReviewRepo(t)
	got := advance(t, f, root, reviewArgs("e1", "")...)
	if !strings.HasPrefix(got, "ignored") {
		t.Fatalf("printed %q; a paused review must not be sent by a repeat event", got)
	}
	if r := load(t, dir).Tasks[0].Review; r == nil || !r.Pending || r.Delivered {
		t.Fatalf("review %+v changed", r)
	}
}

func TestResumeBudgetPausedReviewDoesNotRebuild(t *testing.T) {
	root, dir, _ := pausedReviewRepo(t)
	before := load(t, dir).Tasks[0]
	before.Session = "sess-1"

	if _, rc := runStart(t, root, "100"); rc == 0 {
		t.Fatal("resumed with the budget still spent")
	}
	if r := load(t, dir).Tasks[0].Review; r == nil || !r.Pending {
		t.Fatal("a refused resume must keep the review pending")
	}

	out, rc := runStart(t, root, "1000")
	if rc != 0 {
		t.Fatalf("start rc %d", rc)
	}
	if out["review_pending"] != true || out["resumed"] != true || out["task"] != float64(1) {
		t.Fatalf("start printed %v", out)
	}
	prompt, _ := out["review_prompt"].(string)
	body, err := os.ReadFile(prompt)
	if err != nil || !strings.Contains(string(body), "review of task 1") {
		t.Fatalf("review prompt %q: %v %q", prompt, err, body)
	}
	after := load(t, dir)
	task := after.Tasks[0]
	if task.Status != build.StatusReviewing || task.Review == nil || task.Review.SentAt == "" {
		t.Fatalf("task %+v; the implementation must stay as it was", task)
	}
	if len(task.TreeBefore) != len(before.TreeBefore) || task.Report != before.Report || after.Budget != 1000 || after.State != build.StateRunning {
		t.Fatalf("resume reset the task: %+v", task)
	}

	if rc := cmdBuildReviewSent([]string{"--root", root, "--task", "1"}); rc != 0 {
		t.Fatalf("review-sent rc %d", rc)
	}
	if r := load(t, dir).Tasks[0].Review; r.Pending || !r.Delivered {
		t.Fatalf("review %+v, want delivered and no longer pending", r)
	}
	if _, rc := runStart(t, root, "1000"); rc == 0 {
		t.Fatal("started over a delivered, running review")
	}
}

func TestReviewedTaskOverBudgetIsAcceptedThenStops(t *testing.T) {
	root, dir, f := pausedReviewRepo(t)
	if _, rc := runStart(t, root, "1000"); rc != 0 {
		t.Fatal("resume failed")
	}
	if rc := cmdBuildReviewSent([]string{"--root", root, "--task", "1"}); rc != 0 {
		t.Fatal("review-sent failed")
	}
	// start read the real checkout; the fake tree is what the reviewer sees.
	b := load(t, dir)
	b.Tasks[0].Review.Tree = map[string]string{"src/greet.sh": "h1"}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f.usage = &build.Usage{Input: 2000}
	got := advance(t, f, root, "--task", "1", "--event", "e2", "--created", "2999-01-01T00:00:00Z", "--review", "--verdict", "clean", "--from", "reviewer")
	if !strings.HasPrefix(got, "stopped budget ") {
		t.Fatalf("printed %q", got)
	}
	b = load(t, dir)
	if b.Tasks[0].Status != build.StatusDone || b.Current != 2 || len(f.judged) != 0 {
		t.Fatalf("status %s current %d; the reviewed task is done and the next waits", b.Tasks[0].Status, b.Current)
	}
}
