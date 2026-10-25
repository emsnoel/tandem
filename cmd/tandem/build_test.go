package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/triage"
)

const threeTaskPlan = "# Greeting fixes\n\n**Goal:** greet.\n\n### Task 1: Punctuate\n\none\n\n### Task 2: Farewell\n\ntwo\n\n### Task 3: Review\n\nthree\n"

// buildRepo makes a git repo with a started three-task build.
func buildRepo(t *testing.T, check string, budget int64) (root, dir string) {
	t.Helper()
	return buildRepoPlan(t, threeTaskPlan, check, budget)
}

// buildRepoPlan is buildRepo for a plan of the caller's choosing.
func buildRepoPlan(t *testing.T, planText, check string, budget int64) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", root},
		{"-C", root, "config", "user.email", "t@example.com"},
		{"-C", root, "config", "user.name", "t"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	// The relay is ignored in a real pairing (tandem-pair adds it).
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".tandem-relay/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir = build.Dir(paths.Relay(root))
	// tandem-pair records the reviewer pane; every review goes to it.
	if err := os.MkdirAll(paths.Relay(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Relay(root), "reviewer.json"), []byte(`{"kind":"claude","name":"reviewer","model":"opus","autonomy":"auto"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _, err := build.Start(dir, plan.Split(planText), filepath.Join(root, "p.md"), "sha", build.Options{
		ID: "b1", Builder: "builder", Reviewer: "reviewer", Planner: "planner", Check: check, Budget: budget,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	b.Tasks[0].TreeBefore = map[string]string{}
	b.Tasks[0].Status = build.StatusSent
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	return root, dir
}

type fakeAdvance struct {
	statuses     []string // returned in turn; the last repeats
	calls        int
	checked      []string
	exit         int
	usage        *build.Usage
	clock        time.Time
	dirties      []map[string]string
	dirtyN       int
	judgeVerdict Verdict
	judgeErr     error
	judged       []judgeRequest
	diffText     string
	// incompleteDiff mirrors the live legacy diff, which cannot prove it is
	// the attempt's complete change.
	incompleteDiff bool
	// watchErrs is how many watches break before one works.
	watchErrs int
	// sessions is each pane's conversation; the builder's is sess-1 and the
	// reviewer's sess-r unless set here.
	sessions map[string]string
	// polled names each pane a settle read, in order.
	polled []string
}

func (f *fakeAdvance) deps(t *testing.T) advanceDeps {
	get := func(name string) (*herdr.Agent, error) {
		s := f.statuses[min(f.calls, len(f.statuses)-1)]
		f.calls++
		f.polled = append(f.polled, name)
		session, ok := f.sessions[name]
		if !ok {
			session = "sess-1"
			if name == "reviewer" {
				session = "sess-r"
			}
		}
		return mkAgent(s, session), nil
	}
	return advanceDeps{
		get: get,
		watch: func(names ...string) (eventWatch, error) {
			if f.watchErrs > 0 {
				f.watchErrs--
				a, _ := get(names[0])
				return &scriptWatch{snaps: map[string]*herdr.Agent{names[0]: a}, clock: &f.clock,
					steps: []scriptStep{{after: time.Second, err: &herdr.Error{Code: "unreachable", Unreachable: true}}}}, nil
			}
			return newStepWatch(names[0], get, func(d time.Duration) { f.clock = f.clock.Add(d) })
		},
		sleep: func(d time.Duration) { f.clock = f.clock.Add(d) },
		now:   func() time.Time { return f.clock },
		dirty: func(root string) (map[string]string, error) {
			if len(f.dirties) > 0 {
				i := min(f.dirtyN, len(f.dirties)-1)
				f.dirtyN++
				return f.dirties[i], nil
			}
			return map[string]string{"src/greet.sh": "h1"}, nil
		},
		usage: func(string) *build.Usage { return f.usage },
		runCheck: func(root, command, log string) (int, error) {
			f.checked = append(f.checked, command)
			if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(log, []byte("check output\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return f.exit, nil
		},
		judge: func(root string, req judgeRequest) (Verdict, error) {
			if f.judgeVerdict.RecordID == "" && f.judgeErr == nil {
				t.Fatal("unexpected judge call")
			}
			f.judged = append(f.judged, req)
			id := f.judgeVerdict.RecordID
			if id == "" {
				id = req.ID
			}
			if err := writeJudgeRecord(filepath.Join(root, ".tandem-log"), id, req.TaskID, f.judgeVerdict.Answers, f.judgeVerdict.State, req.CheckPassed, f.judgeVerdict.WouldSkip, recordMeta{TreeHash: req.TreeHash}); err != nil {
				t.Fatal(err)
			}
			return f.judgeVerdict, f.judgeErr
		},
		diff:         func(string, []string) (string, error) { return f.diffText, nil },
		diffComplete: !f.incompleteDiff,
	}
}

func judgeOn(t *testing.T, dir string) {
	t.Helper()
	b := load(t, dir)
	b.Judge = true
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
}
func reviewArgs(id, created string) []string {
	return []string{"--task", "1", "--event", id, "--created", created}
}

// reviewerArgs is the reviewer pane's review handback for task 1.
func reviewerArgs(id, created, verdict string) []string {
	return append(reviewArgs(id, created), "--review", "--verdict", verdict, "--from", "reviewer")
}

// handBackReviewed is the builder's handback of a task followed by the
// reviewer pane's clean verdict on it. Every task is reviewed, so it checks the
// handback opened a review round and returns what the clean verdict printed.
func handBackReviewed(t *testing.T, f *fakeAdvance, root string, task int, extra ...string) string {
	t.Helper()
	n := fmt.Sprint(task)
	if got := advance(t, f, root, append([]string{"--task", n}, extra...)...); !strings.HasPrefix(got, "review "+n+" ") {
		t.Fatalf("task %s handback printed %q, want a review round in the reviewer pane", n, got)
	}
	return advance(t, f, root, "--task", n, "--event", "review-of-"+n, "--review", "--verdict", "clean", "--from", "reviewer")
}

// advance runs the command and returns what it printed.
func advance(t *testing.T, f *fakeAdvance, root string, extra ...string) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	rc := cmdBuildAdvance(f.deps(t), append([]string{"--root", root}, extra...))
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if rc != 0 {
		t.Fatalf("advance rc %d, output %q", rc, out)
	}
	return strings.TrimSpace(string(out))
}

func load(t *testing.T, dir string) *build.Build {
	t.Helper()
	b, err := build.Load(dir)
	if err != nil || b == nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestAdvanceMeasuresTheTaskAndSendsTheNext(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 3000000)
	relay := paths.Relay(root)
	if err := os.WriteFile(filepath.Join(relay, "build-session"), []byte("sess-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"working", "working", "idle"}, usage: &build.Usage{Calls: 9, Input: 300000}}
	if got := handBackReviewed(t, f, root, 1, "--report", "/r/task-1.md"); got != "next 2" {
		t.Fatalf("printed %q", got)
	}
	if f.calls < 3 {
		t.Errorf("did not wait for the builder to settle (%d polls)", f.calls)
	}
	if len(f.checked) != 1 || f.checked[0] != "./check.sh" {
		t.Errorf("check runs: %v", f.checked)
	}
	b := load(t, dir)
	t1 := b.Tasks[0]
	if b.Current != 2 || t1.Status != build.StatusDone || t1.Session != "sess-1" ||
		strings.Join(t1.Files, ",") != "src/greet.sh" || t1.CheckExit == nil || *t1.CheckExit != 0 ||
		t1.Usage == nil || t1.Usage.Calls != 9 || t1.Report != "/r/task-1.md" {
		t.Errorf("task 1 recorded as %+v, current %d", t1, b.Current)
	}
	if _, err := os.Stat(filepath.Join(relay, "build-session")); !os.IsNotExist(err) {
		t.Error("build-session was kept, so the next reset would be skipped")
	}
}

// With triage off, which is the default, Jev is never asked and the reviewer
// pane still reviews every task: "no judge" means "review".
func TestAdvanceWithTriageOffStillReviewsInTheReviewerPane(t *testing.T) {
	t.Setenv("HERDR_JUDGE", "1")
	root, dir := buildRepo(t, "check", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	got := advance(t, f, root, reviewArgs("e1", "2026-09-29T12:00:00Z")...)
	if want := "review 1 " + filepath.Join(dir, "review-1.md"); got != want {
		t.Fatalf("printed %q, want %q", got, want)
	}
	if len(f.judged) != 0 {
		t.Fatalf("judge calls = %d", len(f.judged))
	}
	t1 := load(t, dir).Tasks[0]
	if t1.Status != build.StatusReviewing || t1.Review == nil || t1.Review.Verdict != "review" || t1.Review.Skipped {
		t.Fatalf("task 1 = status %q review %+v, want an open review round", t1.Status, t1.Review)
	}
	// The reviewer's clean verdict accepts the task.
	if got := advance(t, f, root, reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean")...); got != "next 2" {
		t.Fatalf("after the clean review printed %q", got)
	}
}

func TestAdvanceAsksJevWithTheTrueCheckResultAndSendsAReview(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	marker := filepath.Join(paths.Relay(root), "build-session")
	if err := os.WriteFile(marker, []byte("sess-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, clock: time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC), diffText: "diff", judgeVerdict: Verdict{RecordID: "r1"}}
	got := advance(t, f, root, reviewArgs("e1", "2026-09-29T12:00:00Z")...)
	if !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q", got)
	}
	if len(f.judged) != 1 || !f.judged[0].CheckPassed || f.judged[0].Diff != "diff" || f.judged[0].ID != "b1-t1" || !f.judged[0].CheckCommandExists || f.judged[0].CheckOutput != "check output\n" || !strings.Contains(f.judged[0].TaskBrief, "Punctuate") {
		t.Fatalf("requests %+v", f.judged)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "sess-1\n" {
		t.Fatalf("session marker: %q, %v", raw, err)
	}
	msg, err := os.ReadFile(strings.Fields(got)[2])
	if err != nil || !strings.Contains(string(msg), "--review") {
		t.Fatalf("review prompt: %q, %v", msg, err)
	}
	b := load(t, dir)
	if b.Tasks[0].Status != build.StatusReviewing || b.Tasks[0].Review.RecordID != "r1" || b.Current != 1 {
		t.Fatalf("build %+v", b)
	}
}

func TestAdvanceReviewsWhenJevIsUnavailable(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeErr: errors.New("jev down")}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q", got)
	}
	r := load(t, dir).Tasks[0].Review
	if r.Verdict != "unavailable" || r.Reason != "jev down" {
		t.Fatalf("review %+v", r)
	}
}

func TestSecondHandbackRecordsWhetherTheReviewChangedTheTree(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	if err := writeJudgeRecord(filepath.Join(root, ".tandem-log"), "r1", "1", triage.Answers{}, triage.State{}, true, false, recordMeta{}); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, clock: time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC), judgeVerdict: Verdict{RecordID: "r1"}, usage: &build.Usage{}, dirties: []map[string]string{{"src/greet.sh": "h1"}, {"src/greet.sh": "h1"}, {"src/greet.sh": "h2"}}}
	advance(t, f, root, append(reviewArgs("e1", "2026-09-29T12:00:00Z"), "--report", "/r/first.md")...)
	f.clock = f.clock.Add(time.Minute)
	// The reviewer edited a task file: the round is recorded, and not counted.
	if got := advance(t, f, root, append(reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean"), "--report", "/r/review.md")...); !strings.HasPrefix(got, "stopped review_edited_tree ") {
		t.Fatalf("printed %q", got)
	}
	task := load(t, dir).Tasks[0]
	if task.Report != "/r/first.md" || task.ReviewReport != "/r/review.md" {
		t.Fatalf("reports: %+v", task)
	}
	r := task.Review
	if r.Changed == nil || !*r.Changed || r.Reviewed == nil || !*r.Reviewed || len(f.checked) != 2 {
		t.Fatalf("review %+v checks %d", r, len(f.checked))
	}
	var rec judgeRecord
	raw, err := os.ReadFile(filepath.Join(root, ".tandem-log", "judge-r1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &rec); err != nil || rec.ReviewChangedTree == nil || !*rec.ReviewChangedTree || rec.Reviewed == nil || !*rec.Reviewed {
		t.Fatalf("record %+v err %v", rec, err)
	}
}

func TestSecondHandbackWithAnUnchangedTreeDoesNotRerunTheCheck(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}, usage: &build.Usage{}}
	advance(t, f, root, reviewArgs("e1", "")...)
	if got := advance(t, f, root, reviewerArgs("e2", "", "clean")...); got != "next 2" {
		t.Fatalf("printed %q", got)
	}
	if len(f.checked) != 1 {
		t.Fatalf("checks %d", len(f.checked))
	}
}

func TestARepeatOfTheFirstHandbackDuringReviewIsIgnored(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	advance(t, f, root, reviewArgs("e1", "")...)
	b := load(t, dir)
	b.Tasks[0].Review.Delivered = true
	_ = b.Save(dir)
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "ignored") {
		t.Fatalf("printed %q", got)
	}
}

func TestARepeatBeforeDeliveryResendsTheReview(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	first := advance(t, f, root, reviewArgs("e1", "")...)
	if got := advance(t, f, root, reviewArgs("e1", "")...); got != first {
		t.Fatalf("got %q want %q", got, first)
	}
}

// A round no reviewer answered is not a calibration pair: the builder's
// handback does not close it, and nothing records a review.
func TestARoundWithNoReviewerVerdictIsNotAPair(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	advance(t, f, root, reviewArgs("e1", "")...)
	if got := advance(t, f, root, reviewArgs("e2", "")...); !strings.HasPrefix(got, "ignored ") {
		t.Fatalf("the builder's handback closed the round: %q", got)
	}
	r := load(t, dir).Tasks[0].Review
	if r.Reviewed != nil && *r.Reviewed {
		t.Fatalf("review %+v", r)
	}
	if rec := readJudgeRecord(t, root, "r1"); rec.Reviewed != nil && *rec.Reviewed {
		t.Fatalf("record %+v", rec)
	}
}

func TestAFixLoopAfterARedDoesNotAskJevTwice(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	b := load(t, dir)
	reviewed := true
	b.Tasks[0].Review = &build.Review{RecordID: "r1", Reviewed: &reviewed, Result: "clean"}
	b.Tasks[0].Report, b.Tasks[0].ReviewReport = "/r/first.md", "/r/review.md"
	b.Tasks[0].Status = build.StatusRed
	_ = b.Save(dir)
	f := &fakeAdvance{statuses: []string{"idle"}}
	if got := advance(t, f, root, append(reviewArgs("e2", ""), "--report", "/r/fixed.md")...); got != "next 2" {
		t.Fatalf("printed %q", got)
	}
	// The builder's fix report is its own; the reviewer's stays as it was.
	if task := load(t, dir).Tasks[0]; task.Report != "/r/fixed.md" || task.ReviewReport != "/r/review.md" {
		t.Fatalf("reports: %+v", task)
	}
	if len(f.judged) != 0 {
		t.Fatal("judged twice")
	}
}

func TestBuildSentClearsReview(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	b := load(t, dir)
	b.Tasks[0].Review = &build.Review{RecordID: "r1"}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}

	if rc := cmdBuildSent([]string{"--root", root, "--task", "1"}); rc != 0 {
		t.Fatalf("cmdBuildSent rc = %d", rc)
	}
	if got := load(t, dir).Tasks[0].Review; got != nil {
		t.Fatalf("Review = %+v, want nil", got)
	}
}

func TestAdvanceIgnoresATaskThatIsNotCurrent(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	if got := advance(t, f, root, "--task", "2"); !strings.HasPrefix(got, "ignored") {
		t.Fatalf("printed %q", got)
	}
	if f.calls != 0 || load(t, dir).Current != 1 {
		t.Error("a stale event touched the build")
	}
}

func TestAdvanceIgnoresARepeatOfADoneTask(t *testing.T) {
	root, _ := buildRepo(t, "", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	handBackReviewed(t, f, root, 1)
	if got := advance(t, f, root, "--task", "1"); !strings.HasPrefix(got, "ignored") {
		t.Fatalf("a repeated handback printed %q; task 2 would be sent twice", got)
	}
}

func TestAdvanceStopsOnARedCheckAndResumesOnTheNextHandback(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"done"}, exit: 1}
	got := advance(t, f, root, "--task", "1")
	fields := strings.Fields(got)
	if len(fields) != 4 || fields[0] != "stopped" || fields[1] != "red" {
		t.Fatalf("printed %q", got)
	}
	if _, err := os.Stat(filepath.Join(paths.Relay(root), "reports", fields[2]+".md")); err != nil {
		t.Errorf("no handback report for the planner: %v", err)
	}
	msg, err := os.ReadFile(fields[3])
	if err != nil || !strings.Contains(string(msg), "hand back task 1 again") {
		t.Errorf("message: %v\n%s", err, msg)
	}
	b := load(t, dir)
	if b.State != build.StateStopped || b.Current != 1 || b.Tasks[0].Status != build.StatusRed {
		t.Fatalf("state %s current %d status %s", b.State, b.Current, b.Tasks[0].Status)
	}
	// The builder fixes it in the same conversation and hands back again.
	f.exit = 0
	if got := handBackReviewed(t, f, root, 1); got != "next 2" {
		t.Fatalf("after the fix, printed %q", got)
	}
}

func TestAdvanceFinishesAfterTheLastTask(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status, b.Current = build.StatusDone, build.StatusDone, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}}
	got := handBackReviewed(t, f, root, 3)
	if !strings.HasPrefix(got, "finished ") {
		t.Fatalf("printed %q", got)
	}
	if len(f.checked) != 0 {
		t.Error("ran a check with no check command")
	}
	if load(t, dir).State != build.StateFinished {
		t.Error("not recorded as finished")
	}
	// A late repeat after the finish changes nothing.
	if got := advance(t, f, root, "--task", "3"); !strings.HasPrefix(got, "ignored") {
		t.Errorf("printed %q", got)
	}
}

// A reviewed task whose conversation went over budget during its review is
// done, and the build stops before the next task.
func TestAdvanceStopsOverBudgetWithTheTaskDone(t *testing.T) {
	root, dir := buildRepo(t, "", 1000000)
	f := &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{Input: 500000}}
	if got := advance(t, f, root, "--task", "1"); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q", got)
	}
	f.usage = &build.Usage{Input: 2500000}
	if got := advance(t, f, root, reviewerArgs("e2", "", "clean")...); !strings.HasPrefix(got, "stopped budget ") {
		t.Fatalf("printed %q", got)
	}
	b := load(t, dir)
	if b.Tasks[0].Status != build.StatusDone || b.Current != 2 {
		t.Errorf("status %s current %d; a re-handoff must carry on from task 2", b.Tasks[0].Status, b.Current)
	}
}

func TestAdvanceStopsWhenTheBuilderNeverSettles(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"working"}}
	if got := advance(t, f, root, "--task", "1"); !strings.HasPrefix(got, "stopped unsettled ") {
		t.Fatalf("printed %q", got)
	}
	if len(f.checked) != 0 {
		t.Error("ran the check while the builder was still working")
	}
	if b := load(t, dir); b.Tasks[0].Status == build.StatusDone {
		t.Error("a task nobody saw finish was marked done")
	}
}

// A watch that breaks is reopened, so one socket error does not stop a build
// as unsettled.
func TestAdvanceReopensTheWatchAfterAStreamError(t *testing.T) {
	root, _ := buildRepo(t, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"working", "working", "working", "idle"}, watchErrs: 1}
	if got := advance(t, f, root, "--task", "1"); strings.HasPrefix(got, "stopped unsettled ") {
		t.Fatalf("printed %q; a broken watch must be reopened, not end the wait", got)
	}
	if len(f.checked) != 1 {
		t.Errorf("ran the check %d times, want once after the builder settled", len(f.checked))
	}
}

func TestAdvanceRecordsAStuckTask(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	if got := advance(t, f, root, "--task", "1", "--stuck"); !strings.HasPrefix(got, "stopped stuck ") {
		t.Fatalf("printed %q", got)
	}
	if load(t, dir).Tasks[0].Status != build.StatusStuck {
		t.Error("not recorded as stuck")
	}
}

// A stop promises a fresh conversation for the next handoff, so it must remove
// build-session, which makes ResetDecision keep a conversation.
func TestAStopRemovesBuildSession(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra []string
		exit  int
	}{
		{"red", nil, 1},
		{"stuck", []string{"--stuck"}, 0},
	} {
		root, _ := buildRepo(t, "./check.sh", 0)
		session := filepath.Join(paths.Relay(root), "build-session")
		if err := os.WriteFile(session, []byte("sess-1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := &fakeAdvance{statuses: []string{"idle"}, exit: c.exit}
		if got := advance(t, f, root, append([]string{"--task", "1"}, c.extra...)...); !strings.HasPrefix(got, "stopped ") {
			t.Fatalf("%s: printed %q", c.name, got)
		}
		if _, err := os.Stat(session); !os.IsNotExist(err) {
			t.Errorf("%s: build-session kept after a stop; the next handoff would not reset", c.name)
		}
	}
}

func TestAFinishRemovesBuildSession(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status, b.Current = build.StatusDone, build.StatusDone, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(paths.Relay(root), "build-session")
	if err := os.WriteFile(session, []byte("sess-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}}
	if got := handBackReviewed(t, f, root, 3); !strings.HasPrefix(got, "finished ") {
		t.Fatalf("printed %q", got)
	}
	if _, err := os.Stat(session); !os.IsNotExist(err) {
		t.Error("build-session kept after the build finished")
	}
}

func TestAStopWithNoBuildSessionIsFine(t *testing.T) {
	root, _ := buildRepo(t, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"idle"}, exit: 1}
	if got := advance(t, f, root, "--task", "1"); !strings.HasPrefix(got, "stopped red ") {
		t.Fatalf("printed %q", got)
	}
}

// accept runs the command and returns rc and what it printed.
func accept(t *testing.T, root string, extra ...string) (int, string) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	rc := cmdBuildAccept(append([]string{"--root", root}, extra...))
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return rc, strings.TrimSpace(string(out))
}

func TestAcceptMarksAStuckTaskDoneWithItsReason(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	advance(t, f, root, "--task", "1", "--stuck")
	rc, got := accept(t, root, "--task", "1", "--because", "check passed; the sandbox killed the builder's run")
	if rc != 0 || !strings.HasPrefix(got, "accepted 1; run tandem-handoff ") || !strings.HasSuffix(got, "to send task 2") {
		t.Fatalf("rc %d printed %q", rc, got)
	}
	b := load(t, dir)
	t1 := b.Tasks[0]
	if t1.Status != build.StatusDone || t1.AcceptedFrom != build.StatusStuck ||
		t1.Accepted != "check passed; the sandbox killed the builder's run" || b.Current != 2 {
		t.Errorf("task 1 %+v, current %d", t1, b.Current)
	}
	// A late handback of the accepted task moves nothing.
	if got := advance(t, f, root, "--task", "1"); !strings.HasPrefix(got, "ignored") {
		t.Errorf("a late handback printed %q", got)
	}
}

func TestAcceptOnTheLastTaskFinishesTheBuild(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status, b.Current = build.StatusDone, build.StatusDone, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	advance(t, &fakeAdvance{statuses: []string{"idle"}, exit: 1}, root, "--task", "3")
	rc, got := accept(t, root, "--task", "3", "--because", "red by design")
	fields := strings.Fields(got)
	if rc != 0 || len(fields) != 3 || fields[0] != "finished" {
		t.Fatalf("rc %d printed %q", rc, got)
	}
	if _, err := os.Stat(filepath.Join(paths.Relay(root), "reports", fields[1]+".md")); err != nil {
		t.Errorf("no report for tandem-unpair --verified: %v", err)
	}
	if b := load(t, dir); b.State != build.StateFinished || b.Tasks[2].AcceptedFrom != build.StatusRed {
		t.Errorf("state %s, task 3 %+v", b.State, b.Tasks[2])
	}
}

func TestAcceptRefusesWhatItCannotAccept(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"a running build", []string{"--task", "1", "--because", "x"}},
		{"no reason", []string{"--task", "1"}},
		{"a blank reason", []string{"--task", "1", "--because", "  "}},
	} {
		if rc, _ := accept(t, root, c.args...); rc != 2 {
			t.Errorf("%s: rc %d, want 2", c.name, rc)
		}
	}
	advance(t, &fakeAdvance{statuses: []string{"idle"}, exit: 1}, root, "--task", "1")
	if rc, _ := accept(t, root, "--task", "2", "--because", "x"); rc != 2 {
		t.Errorf("accepting a task the build did not stop at: rc %d, want 2", rc)
	}
	after := load(t, dir)
	if after.Tasks[0].Status != build.StatusRed || after.Tasks[0].Accepted != "" {
		t.Errorf("a refused accept changed task 1: %+v", after.Tasks[0])
	}
}

// accept finds the repository from the working directory when --root is
// omitted, as the stop message prints it.
func TestAcceptFindsTheRootFromTheWorkingDirectory(t *testing.T) {
	root, dir := buildRepo(t, "./check.sh", 0)
	advance(t, &fakeAdvance{statuses: []string{"idle"}}, root, "--task", "1", "--stuck")
	wd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join(root, ".tandem-relay")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if rc := cmdBuildAccept([]string{"--task", "1", "--because", "done as it stands"}); rc != 0 {
		t.Fatalf("accept without --root: rc %d", rc)
	}
	if load(t, dir).Tasks[0].Accepted != "done as it stands" {
		t.Error("the acceptance was not recorded")
	}
}

func TestAdvanceKeepsACleanVerdictWhileStillReviewing(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: confidentVerdict("r1")}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatal(got)
	}
	if r := load(t, dir).Tasks[0].Review; r.Verdict != "clean" {
		t.Fatalf("review %+v", r)
	}
}

func TestARepeatBeforeDeliveryDoesNotResendAnOlderDifferentEvent(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, clock: time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC), judgeVerdict: Verdict{RecordID: "r1"}}
	advance(t, f, root, reviewArgs("e1", "2026-09-29T12:00:00Z")...)
	if got := advance(t, f, root, reviewArgs("old-event", "2026-09-29T12:00:00Z")...); got != "ignored a repeat of the first handback" {
		t.Fatal(got)
	}
	if b := load(t, dir); b.Tasks[0].Status != build.StatusReviewing || len(f.checked) != 1 {
		t.Fatalf("build %+v", b)
	}
}

func TestBuildStopNamesTheRightTaskAndLetsAcceptResolveIt(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	advance(t, f, root, reviewArgs("e1", "")...)
	if rc := cmdBuild([]string{"stop", "--root", root, "--task", "1", "--reason", "review-delivery"}); rc != 0 {
		t.Fatalf("stop: %d", rc)
	}
	if b := load(t, dir); b.StopTask != 1 || b.Tasks[0].Status != build.StatusStuck {
		t.Fatalf("build %+v", b)
	}
	if rc := cmdBuildAccept([]string{"--root", root, "--task", "1", "--because", "reviewed manually"}); rc != 0 {
		t.Fatalf("accept: %d", rc)
	}
	if b := load(t, dir); b.Current != 2 || b.Tasks[0].Status != build.StatusDone {
		t.Fatalf("build %+v", b)
	}
}

func TestBuildStopWithoutTaskKeepsTheCompletedTaskDone(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	handBackReviewed(t, f, root, 1)
	if rc := cmdBuildStop([]string{"--root", root}); rc != 0 {
		t.Fatalf("stop: %d", rc)
	}
	if b := load(t, dir); b.StopTask != 1 || b.Tasks[0].Status != build.StatusDone {
		t.Fatalf("build %+v", b)
	}
}

func TestBuildReviewSentRecordsDelivery(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	advance(t, f, root, reviewArgs("e1", "")...)
	if rc := cmdBuild([]string{"review-sent", "--root", root, "--task", "1"}); rc != 0 {
		t.Fatalf("review-sent: %d", rc)
	}
	if !load(t, dir).Tasks[0].Review.Delivered {
		t.Fatal("delivery not recorded")
	}
}

func TestTheChecksOwnFileWritesAreNotReviewerWork(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}, dirties: []map[string]string{
		{"src/greet.sh": "h1"},
		{"src/greet.sh": "h1", "check-output": "written-by-check"},
		{"src/greet.sh": "h1", "check-output": "changed-outside-task"},
	}}
	advance(t, f, root, reviewArgs("e1", "")...)
	advance(t, f, root, reviewerArgs("e2", "", "clean")...)
	r := load(t, dir).Tasks[0].Review
	// The check's own file stays out of the reviewer's attribution. It still
	// changed after the check ran, so the check reruns.
	if r.Changed == nil || *r.Changed || strings.Join(r.OtherPaths, ",") != "check-output" || len(f.checked) != 2 {
		t.Fatalf("review %+v, checks %d", r, len(f.checked))
	}
}

// A file outside the task that changed during the review makes the earlier
// check stale. Run again, it fails, and the task stops red.
func TestReviewThatLeavesTheCheckRedStopsRed(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	b := load(t, dir)
	b.Tasks[0].TreeBefore = map[string]string{"vendor/dep.go": "d1"}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}, usage: &build.Usage{}, dirties: []map[string]string{
		{"src/greet.sh": "h1", "vendor/dep.go": "d1"}, {"src/greet.sh": "h1", "vendor/dep.go": "d1"}, {"src/greet.sh": "h1", "vendor/dep.go": "d2"}}}
	advance(t, f, root, reviewArgs("e1", "")...)
	f.exit = 1
	if got := advance(t, f, root, reviewerArgs("e2", "", "clean")...); !strings.HasPrefix(got, "stopped red ") {
		t.Fatal(got)
	}
	if b := load(t, dir); b.Tasks[0].Status != build.StatusRed || b.Current != 1 {
		t.Fatalf("build %+v", b)
	}
	f.exit = 0
	if got := advance(t, f, root, reviewArgs("e3", "")...); got != "next 2" || len(f.judged) != 1 {
		t.Fatalf("fix: %q, judged %d", got, len(f.judged))
	}
}

// A builder that cannot finish the reviewer's findings stops the build; its
// next handback opens a new round, since the findings were never reviewed fixed.
func TestStuckInAFixRoundStopsStuck(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}, usage: &build.Usage{}}
	advance(t, f, root, reviewArgs("e1", "")...)
	if got := advance(t, f, root, append(reviewerArgs("e2", "", "changes"), "--report", "/r/review.md")...); !strings.HasPrefix(got, "fix 1 ") {
		t.Fatal(got)
	}
	if got := advance(t, f, root, append(reviewArgs("e3", ""), "--stuck")...); !strings.HasPrefix(got, "stopped stuck ") {
		t.Fatal(got)
	}
	if b := load(t, dir); b.Tasks[0].Status != build.StatusStuck || b.Current != 1 {
		t.Fatalf("build %+v", b)
	}
	if got := advance(t, f, root, reviewArgs("e4", "")...); !strings.HasPrefix(got, "review 1 ") || len(f.judged) != 2 {
		t.Fatalf("fix: %q, judged %d", got, len(f.judged))
	}
}

func TestARedFirstCheckNeverAsksJevAndNeverReviews(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, exit: 1}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "stopped red ") {
		t.Fatal(got)
	}
	if b := load(t, dir); b.Tasks[0].Status != build.StatusRed || b.Tasks[0].Review != nil || len(f.judged) != 0 {
		t.Fatalf("build %+v", b)
	}
}

func TestAdvanceBudgetStopDoesNotAskJev(t *testing.T) {
	root, dir := buildRepo(t, "check", 100)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{Input: 101}}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "stopped budget ") {
		t.Fatal(got)
	}
	// Spending stops the loop; it does not accept an unreviewed task.
	if b := load(t, dir); b.Current != 1 || b.Tasks[0].Status != build.StatusReviewing || len(f.judged) != 0 {
		t.Fatalf("build %+v", b)
	}
}

func TestBuildCarriesBothReportsIntoTheLedgerAndPlannerReport(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	b := load(t, dir)
	task := &b.Tasks[0]
	task.Status = build.StatusDone
	task.Report = filepath.Join(root, "first.md")
	task.ReviewReport = filepath.Join(root, "second.md")
	if err := os.WriteFile(task.Report, []byte(strings.Repeat("first note\n", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task.ReviewReport, []byte("review found a bug"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, reviews := reportTexts(b)
	for name, text := range map[string]string{"ledger": build.Ledger(b, notes, reviews), "report": build.Report(b, notes, reviews)} {
		if !strings.Contains(text, "first note") || !strings.Contains(text, "review found a bug") {
			t.Fatalf("%s lost a note: %s", name, text)
		}
	}
}

func TestAdvanceRecordsRealJudgeFailuresAndStillReviews(t *testing.T) {
	for _, tc := range []struct {
		name, key, reason string
		client            *fakeJudge
	}{
		{"missing key", "", "TYPESAFE_API_KEY", &fakeJudge{}},
		{"outage", "ts-test", "provider call failed", &fakeJudge{err: errors.New("offline")}},
		{"malformed", "ts-test", "response could not be read", &fakeJudge{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TYPESAFE_API_KEY", tc.key)
			useFakeJudge(t, tc.client)
			root, dir := buildRepo(t, "check", 0)
			judgeOn(t, dir)
			f := &fakeAdvance{statuses: []string{"idle"}}
			d := f.deps(t)
			d.judge = liveAdvanceDeps.judge
			if rc := cmdBuildAdvance(d, append([]string{"--root", root}, reviewArgs("e1", "")...)); rc != 0 {
				t.Fatalf("advance: %d", rc)
			}
			r := load(t, dir).Tasks[0].Review
			if r == nil || r.Verdict != "unavailable" || !strings.Contains(r.Reason, tc.reason) {
				t.Fatalf("review %+v", r)
			}
			rec := readJudgeRecord(t, root, "b1-t1")
			if rec.Verdict != "unavailable" || !strings.Contains(rec.Unavailable, tc.reason) || !rec.CheckPassed {
				t.Fatalf("record %+v", rec)
			}
		})
	}
}

func TestAdvanceOffClearsAnOldRedResultWhenTheCheckIsRemoved(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	f := &fakeAdvance{statuses: []string{"idle"}, exit: 1}
	if got := advance(t, f, root, "--task", "1"); !strings.HasPrefix(got, "stopped red ") {
		t.Fatal(got)
	}
	b := load(t, dir)
	b.Check = ""
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	if got := handBackReviewed(t, f, root, 1); got != "next 2" {
		t.Fatal(got)
	}
	if task := load(t, dir).Tasks[0]; task.CheckExit != nil || task.Status != build.StatusDone {
		t.Fatalf("task %+v", task)
	}
	if len(f.checked) != 1 {
		t.Fatalf("checks %d", len(f.checked))
	}
}

func TestBuildStartReportsAndPreservesJudge(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			root, dir := buildRepo(t, "", 0)
			planPath := filepath.Join(root, "p.md")
			if err := os.WriteFile(planPath, []byte(threeTaskPlan), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"--plan", planPath, "--root", root}
			if initial {
				args = append(args, "--judge")
			}
			// Only the flag matters to the CLI, never its process environment.
			t.Setenv("HERDR_JUDGE", "1")
			t.Setenv("TYPESAFE_API_KEY", "k")
			for _, dry := range []bool{true, false} {
				callArgs := append([]string(nil), args...)
				if dry {
					callArgs = append(callArgs, "--dry-run")
				}
				out := captureStdout(t, func() int {
					rc := cmdBuildStart(callArgs)
					if rc != 0 {
						t.Fatalf("start exit %d", rc)
					}
					return rc
				})
				var got startOut
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if got.Judge != initial || got.Resumed {
					t.Fatalf("new build: %+v", got)
				}
			}
			args = []string{"--plan", planPath, "--root", root, "--resume-only"}
			if !initial {
				args = append(args, "--judge")
			}
			out := captureStdout(t, func() int { return cmdBuildStart(args) })
			var got startOut
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			stored, err := build.Load(dir)
			if err != nil || !got.Resumed || got.Judge != initial || stored.Judge != initial {
				t.Fatalf("resume output=%+v stored=%+v err=%v", got, stored, err)
			}
		})
	}
}

// seedPairs seeds complete, matched calibration evidence under the current
// cohort. It includes changes only in the review verdicts, so none is a miss.
func seedPairs(t *testing.T, root string, skipPairs, changed int) {
	t.Helper()
	logDir := filepath.Join(root, ".tandem-log")
	relay := filepath.Join(root, ".tandem-relay")
	if err := os.MkdirAll(relay, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(relay, "reviewer.json")); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(relay, "reviewer.json"), []byte(`{"kind":"claude","name":"reviewer","model":"opus"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < skipPairs+changed; i++ {
		id := fmt.Sprintf("seed-%d", i)
		wouldSkip, changedTree, reviewed := i < skipPairs, i >= skipPairs, true
		if err := writeJudgeRecord(logDir, id, id, triage.Answers{}, triage.State{TaskBrief: id}, true, wouldSkip, recordMeta{TreeHash: "tree-" + id}); err != nil {
			t.Fatal(err)
		}
		if err := updateJudgeRecord(logDir, id, func(rec *judgeRecord) {
			rec.ContractVersion, rec.PolicyVersion, rec.Model = decision.ContractVersion, decision.PolicyVersion, decision.Model
			rec.Reviewed, rec.ReviewChangedTree = &reviewed, &changedTree
			completeEvidence(rec, currentReviewerConfig(root), "tree-"+id)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// pickID returns a record id the audit rule does and does not hold out, so these
// tests do not depend on how a particular string happens to hash.
func pickID(held bool) string {
	for i := 0; ; i++ {
		id := fmt.Sprintf("job-%d", i)
		if triage.AuditSelected(id, triage.PolicyHash()) == held {
			return id
		}
	}
}

var (
	unaudited = pickID(false)
	audited   = pickID(true)
)

func TestSkipNeedsTheSwitchAndAPassingReport(t *testing.T) {
	for _, tc := range []struct {
		name, switchValue, recordID, check string
		skipPairs, changed                 int
		wouldSkip, unavailable, miss, old  bool
		wantSkip                           bool
	}{
		{name: "earned skip", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true, wantSkip: true},
		{name: "switch off", recordID: unaudited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true},
		{name: "switch must equal one", switchValue: "true", recordID: unaudited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true},
		{name: "only 29 skip pairs", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 29, changed: 10, wouldSkip: true},
		{name: "only nine changes", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 30, changed: 9, wouldSkip: true},
		{name: "review verdict", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 30, changed: 10},
		// SHA-256(audited) modulo 5 is 0; SHA-256(unaudited) modulo 5 is 1.
		{name: "audit sample", switchValue: "1", recordID: audited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true},
		{name: "no check", switchValue: "1", recordID: unaudited, skipPairs: 30, changed: 10, wouldSkip: true},
		{name: "provider failure", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true, unavailable: true},
		{name: "miss closes gate", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true, miss: true},
		{name: "old evidence", switchValue: "1", recordID: unaudited, check: "check", skipPairs: 30, changed: 10, wouldSkip: true, old: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_TRIAGE_SKIP", tc.switchValue)
			root, dir := buildRepo(t, tc.check, 0)
			judgeOn(t, dir)
			seedPairs(t, root, tc.skipPairs, tc.changed)
			if tc.miss || tc.old {
				if err := updateJudgeRecord(filepath.Join(root, ".tandem-log"), "seed-0", func(rec *judgeRecord) {
					if tc.miss {
						yes := true
						rec.ReviewChangedTree = &yes
					} else {
						rec.Identity.JudgeConfigHash = "an-older-judge"
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: verdictFor(tc.recordID, tc.wouldSkip)}
			if tc.unavailable {
				f.judgeErr = errors.New("provider down")
			}
			got := advance(t, f, root, reviewArgs("e1", "")...)
			b := load(t, dir)
			if tc.wantSkip {
				if got != "next 2" || b.Current != 2 || b.Tasks[0].Status != build.StatusDone {
					t.Fatalf("output %q, build %+v", got, b)
				}
				r := b.Tasks[0].Review
				if r == nil || r.Verdict != "clean" || r.Reason != "skipped by the triage gate" || r.Changed != nil || r.Reviewed != nil {
					t.Fatalf("review %+v", r)
				}
				if _, err := os.Stat(filepath.Join(dir, "review-1.md")); !os.IsNotExist(err) {
					t.Fatalf("skip wrote a review prompt: %v", err)
				}
			} else if !strings.HasPrefix(got, "review 1 ") || b.Current != 1 || b.Tasks[0].Status != build.StatusReviewing {
				t.Fatalf("output %q, build %+v", got, b)
			}
		})
	}
}

func TestASkippedTaskDoesNotBecomeAPair(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: confidentVerdict(unaudited)}
	if got := advance(t, f, root, reviewArgs("e1", "")...); got != "next 2" {
		t.Fatal(got)
	}
	rec := readJudgeRecord(t, root, unaudited)
	if rec.ReviewChangedTree != nil || rec.Reviewed != nil {
		t.Fatalf("skipped task acquired review facts: %+v", rec)
	}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "ignored ") || len(f.judged) != 1 {
		t.Fatalf("repeat output %q, judge calls %d", got, len(f.judged))
	}
	got := captureStdout(t, func() int { return cmdTriageReport([]string{"--root", root, "--json"}) })
	var report triage.Report
	if err := json.Unmarshal([]byte(got), &report); err != nil {
		t.Fatal(err)
	}
	if report.Pairs != 40 || report.SkipPairs != 30 || report.Excluded != 1 {
		t.Fatalf("report %+v", report)
	}
}

func TestSkipNeverAppliesToARedCheck(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}, exit: 1, judgeVerdict: confidentVerdict(unaudited)}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "stopped red ") {
		t.Fatal(got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusRed || task.Review != nil || len(f.judged) != 0 {
		t.Fatalf("task %+v, judge calls %d", task, len(f.judged))
	}
}

func TestSkipNeverAppliesToAStuckTask(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: confidentVerdict(unaudited)}
	if got := advance(t, f, root, append(reviewArgs("e1", ""), "--stuck")...); !strings.HasPrefix(got, "stopped stuck ") {
		t.Fatal(got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusStuck || task.Review != nil || len(f.judged) != 0 {
		t.Fatalf("task %+v, judge calls %d", task, len(f.judged))
	}
}

func TestSkipCanFinishTheLastTask(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root, 30, 10)
	b := load(t, dir)
	b.Tasks[0].Status, b.Tasks[1].Status, b.Current = build.StatusDone, build.StatusDone, 3
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: confidentVerdict(unaudited)}
	if got := advance(t, f, root, "--task", "3"); !strings.HasPrefix(got, "finished ") {
		t.Fatal(got)
	}
	if b := load(t, dir); b.State != build.StateFinished || b.Tasks[2].Status != build.StatusDone {
		t.Fatalf("build %+v", b)
	}
}

func TestSkipAuditMissClosesTheGateForTheNextTask(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}, usage: &build.Usage{}, judgeVerdict: confidentVerdict(audited)}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatal(got)
	}
	// The audited task would have been skipped, and its reviewer asked for
	// changes: a miss.
	if got := advance(t, f, root, append(reviewerArgs("e2", "", "changes"), "--report", "/r/review.md")...); !strings.HasPrefix(got, "fix 1 ") {
		t.Fatal(got)
	}
	rec := readJudgeRecord(t, root, audited)
	if rec.Reviewed == nil || !*rec.Reviewed || rec.ReviewChangedTree == nil || !*rec.ReviewChangedTree {
		t.Fatalf("audit review facts %+v", rec)
	}
	f.judgeVerdict.RecordID = unaudited
	if got := advance(t, f, root, reviewArgs("e3", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("miss did not close gate for the fixed task: %q", got)
	}
	if got := advance(t, f, root, reviewerArgs("e4", "", "clean")...); got != "next 2" {
		t.Fatal(got)
	}
	if got := advance(t, f, root, "--task", "2", "--event", "e5"); !strings.HasPrefix(got, "review 2 ") {
		t.Fatalf("miss did not close gate: %q", got)
	}
}

func TestSkipSwitchDoesNotEnableJudging(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	seedPairs(t, root, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatal(got)
	}
	if task := load(t, dir).Tasks[0]; task.Review == nil || task.Review.Skipped || len(f.judged) != 0 {
		t.Fatalf("judging enabled without stored switch: task %+v, calls %d", task, len(f.judged))
	}
}
