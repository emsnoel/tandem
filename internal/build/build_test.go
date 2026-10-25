package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/plan"
)

const planText = "# Greeting fixes\n\n**Goal:** greet.\n\n### Task 1: Punctuate\n\none\n\n### Task 2: Farewell\n\ntwo\n\n### Task 3: Review\n\nthree\n"

func opts() Options {
	return Options{ID: "b1", Builder: "builder", Planner: "planner", Check: "./check.sh", Budget: 3000000, Now: "2026-09-28T23:00:00Z", HistoryRoot: histRoot}
}

func start(t *testing.T, dir string, sha string, dry bool) (*Build, bool) {
	t.Helper()
	b, resumed, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", sha, opts(), dry)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return b, resumed
}

func TestStartWritesTheRecordAndOneBriefPerTask(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, resumed := start(t, dir, "sha1", false)
	if resumed || b.Current != 1 || b.State != StateRunning || len(b.Tasks) != 3 {
		t.Fatalf("got resumed=%v %+v", resumed, b)
	}
	for i, task := range b.Tasks {
		if task.Status != StatusPending {
			t.Errorf("task %d status %q", i+1, task.Status)
		}
		body, err := os.ReadFile(task.Brief)
		if err != nil {
			t.Fatalf("brief %d: %v", i+1, err)
		}
		if !strings.Contains(string(body), "**Goal:** greet.") {
			t.Errorf("brief %d lacks the preamble", i+1)
		}
	}
	got, err := Load(dir)
	if err != nil || got == nil || got.PlanSHA != "sha1" || got.Title != "Greeting fixes" {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}

func TestStartResumesAtTheFirstTaskNotDone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	b.Tasks[0].Status = StatusDone
	b.Tasks[1].Status = StatusRed
	b.State, b.Current = StateStopped, 2
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	b, resumed := start(t, dir, "sha1", false)
	if !resumed || b.Current != 2 || b.State != StateRunning {
		t.Fatalf("resumed=%v current=%d state=%s; the red task must be sent again", resumed, b.Current, b.State)
	}
}

func TestStartRefusesToResumeWhileATaskIsInReview(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	b.Tasks[0].Status = StatusReviewing
	b.Tasks[0].Review = &Review{RecordID: "r1"}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}

	_, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha1", opts(), false)
	if err == nil || !strings.Contains(err.Error(), "task 1 is in review") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestStartBeginsAgainWhenThePlanChanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	b.Tasks[0].Status = StatusDone
	b.Current = 2
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	b, resumed := start(t, dir, "sha2", false)
	if resumed || b.Current != 1 || b.Tasks[0].Status != StatusPending {
		t.Fatalf("resumed=%v current=%d", resumed, b.Current)
	}
}

func TestStartBeginsAgainAfterAFinishedBuild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	b.State = StateFinished
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, resumed := start(t, dir, "sha1", false); resumed {
		t.Fatal("a finished build was resumed")
	}
}

func TestStartDryRunWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", true)
	if b.Current != 1 {
		t.Fatalf("current = %d", b.Current)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dry run created %s", dir)
	}
}

func TestLoadOfNoBuildIsNil(t *testing.T) {
	b, err := Load(filepath.Join(t.TempDir(), "build"))
	if b != nil || err != nil {
		t.Fatalf("got %+v, %v", b, err)
	}
}

func TestStartResumeKeepsTheStoredJudgeSwitch(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "build")
			o := opts()
			o.Judge = initial
			if _, _, err := Start(dir, plan.Split(planText), "/p.md", "sha", o, false); err != nil {
				t.Fatal(err)
			}
			o.Judge = !initial
			for _, dry := range []bool{true, false} {
				b, resumed, err := Start(dir, plan.Split(planText), "/p.md", "sha", o, dry)
				if err != nil || !resumed || b.Judge != initial {
					t.Fatalf("dry=%v: resumed=%v judge=%v err=%v", dry, resumed, b.Judge, err)
				}
			}
			b, err := Load(dir)
			if err != nil || b.Judge != initial {
				t.Fatalf("stored judge changed: %+v %v", b, err)
			}
		})
	}
}

func reviewingBuild(t *testing.T, dir string, review *Review, input int64) {
	t.Helper()
	b, _ := start(t, dir, "sha1", false)
	b.Tasks[0].Status, b.Tasks[0].Review, b.Tasks[0].Usage = StatusReviewing, review, &Usage{Input: input}
	b.State, b.StopReason = StateStopped, "budget"
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
}

func TestStartResumesAReviewThatBudgetPaused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	reviewingBuild(t, dir, &Review{Pending: true, Verdict: "review"}, 3000000)
	o := opts()
	o.Budget = 5000000
	b, resumed, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha1", o, false)
	if err != nil || !resumed {
		t.Fatalf("resumed=%v err=%v", resumed, err)
	}
	if b.Current != 1 || b.Tasks[0].Status != StatusReviewing || b.Tasks[0].Review == nil || !b.Tasks[0].Review.Pending {
		t.Fatalf("the paused review was reset: current %d %+v", b.Current, b.Tasks[0])
	}
}

func TestStartRefusesAPausedReviewWhileTheBudgetIsStillSpent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	reviewingBuild(t, dir, &Review{Pending: true}, 3000000)
	if _, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha1", opts(), false); err == nil || !strings.Contains(err.Error(), "waiting for budget") {
		t.Fatalf("err %v", err)
	}
}

func TestStartStillRefusesADeliveredReview(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	reviewingBuild(t, dir, &Review{Delivered: true}, 1)
	if _, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha1", opts(), false); err == nil || !strings.Contains(err.Error(), "in review") {
		t.Fatalf("err %v", err)
	}
}

func TestPlannerMessageForABudgetPausedReviewSaysTheTaskIsNotAccepted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	reviewingBuild(t, dir, &Review{Pending: true}, 3000000)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.StopTask = 1
	got := PlannerMessage(b, "R1", "/r/R1.md")
	if !strings.Contains(got, "review has\nnot run") || strings.Contains(got, "The task itself is\ndone") {
		t.Fatalf("message %q", got)
	}
}

func TestStartRecordsTheReviewerPane(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	o := opts()
	o.Reviewer = "reviewer"
	b, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha1", o, false)
	if err != nil || b.Reviewer != "reviewer" {
		t.Fatalf("new build reviewer %q, err %v", b.Reviewer, err)
	}
	if got, _ := Load(dir); got.Reviewer != "reviewer" {
		t.Fatalf("stored reviewer %q", got.Reviewer)
	}
	// A resume types reviews into the pane tandem-pair recorded now.
	o.Reviewer = "reviewer-2"
	b, resumed, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha1", o, false)
	if err != nil || !resumed || b.Reviewer != "reviewer-2" {
		t.Fatalf("resumed=%v reviewer %q err %v", resumed, b.Reviewer, err)
	}
}

func TestPlannerMessageForAReviewerThatEditedTheTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	b.Reviewer = "reviewer"
	b.State, b.StopReason, b.StopTask = StateStopped, "review_edited_tree", 1
	b.Tasks[0].Status = StatusStuck
	got := PlannerMessage(b, "R1", "/r/R1.md")
	for _, want := range []string{"reviewer changed the tree", "not accepted", "tandem build accept --task 1 --because", "git diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
}

func TestPlannerMessageForReviewRoundsSpent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	b.Reviewer = "reviewer"
	b.State, b.StopReason, b.StopTask = StateStopped, "review_rounds", 1
	b.Tasks[0].Status, b.Tasks[0].ReviewRounds = StatusStuck, MaxReviewRounds
	b.Tasks[0].ReviewReport = "/relay/build/task-1.review.md"
	got := PlannerMessage(b, "R1", "/r/R1.md")
	for _, want := range []string{fmt.Sprintf("%d review rounds", MaxReviewRounds), "/relay/build/task-1.review.md", "not accepted", "tandem build accept --task 1 --because", "tandem-ask builder"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
}

func TestPlannerMessageForABudgetPausedReviewNamesTheReviewer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	reviewingBuild(t, dir, &Review{Pending: true}, 3000000)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.Reviewer, b.StopTask = "reviewer", 1
	if got := PlannerMessage(b, "R1", "/r/R1.md"); !strings.Contains(got, "send the review to reviewer") {
		t.Fatalf("message %q", got)
	}
}

func TestPlannerMessageForAFixHeldByTheBudget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	b, _ := start(t, dir, "sha1", false)
	yes := true
	b.Reviewer, b.Budget = "critic", 100
	b.State, b.StopReason, b.StopTask = StateStopped, "budget", 1
	b.Tasks[0].Status, b.Tasks[0].Usage = StatusStuck, &Usage{Input: 150}
	b.Tasks[0].Review = &Review{Reviewed: &yes, Result: "changes"}
	b.Tasks[0].ReviewReport = "/relay/build/task-1.review.md"
	got := PlannerMessage(b, "R1", "/r/R1.md")
	for _, want := range []string{"critic asked for changes", "the fix was not sent", "not accepted", "/relay/build/task-1.review.md",
		"tandem-ask builder", "tandem build accept --task 1 --because"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "The task itself is\ndone") {
		t.Errorf("an unaccepted task is called done:\n%s", got)
	}
}
