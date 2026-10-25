package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// The legacy review round runs in the reviewer pane. The builder never
// reviews: its handback opens the round, the reviewer's verdict closes it.

func TestBuildStartRefusesWithoutAReviewer(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	planPath := filepath.Join(root, "p.md")
	if err := os.WriteFile(planPath, []byte(threeTaskPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(paths.Relay(root), "reviewer.json")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "build.json"))
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		rc, out, errText := runCapture(t, append([]string{"build", "start", "--plan", planPath, "--root", root}, extra...)...)
		if rc != exitcode.ManualGate || out != "" {
			t.Fatalf("%v: rc %d, stdout %q; want %d and nothing started", extra, rc, out, exitcode.ManualGate)
		}
		for _, want := range []string{"builder never reviews", `"reviewer" block in .tandem.json`, "tandem-pair --reviewer KIND"} {
			if !strings.Contains(errText, want) {
				t.Errorf("%v: the refusal lacks %q:\n%s", extra, want, errText)
			}
		}
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "build.json")); string(after) != string(before) {
		t.Fatal("a refused start changed the build")
	}
}

// A reviewer named the same as the builder or the planner is that agent, and
// would review its own work or plan. ValidateReviewerPane's rule refuses it.
func TestBuildStartRefusesAReviewerNamedAsTheBuilderOrPlanner(t *testing.T) {
	for _, name := range []string{"builder", "planner"} {
		root, dir := buildRepo(t, "", 0)
		planPath := filepath.Join(root, "p.md")
		if err := os.WriteFile(planPath, []byte(threeTaskPlan), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(paths.Relay(root), "reviewer.json"), []byte(`{"kind":"codex","name":"`+name+`"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(dir, "build.json"))
		rc, out, errText := runCapture(t, "build", "start", "--plan", planPath, "--root", root)
		if rc != exitcode.ManualGate || out != "" {
			t.Fatalf("%s: rc %d, stdout %q; want %d and nothing started", name, rc, out, exitcode.ManualGate)
		}
		for _, want := range []string{"the reviewer pane " + name + " is the " + name, `"reviewer" block in .tandem.json`, "tandem-pair --reviewer KIND"} {
			if !strings.Contains(errText, want) {
				t.Errorf("%s: the refusal lacks %q:\n%s", name, want, errText)
			}
		}
		if after, _ := os.ReadFile(filepath.Join(dir, "build.json")); string(after) != string(before) {
			t.Fatalf("%s: a refused start changed the build", name)
		}
	}
}

func TestBuildStartRecordsTheReviewerPane(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	planPath := filepath.Join(root, "p.md")
	if err := os.WriteFile(planPath, []byte(threeTaskPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Relay(root), "reviewer.json"), []byte(`{"kind":"codex","name":"critic"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out, errText := runCapture(t, "build", "start", "--plan", planPath, "--root", root)
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, errText)
	}
	if got := oneJSON(t, out); got["reviewer"] != "critic" {
		t.Fatalf("start printed %v", got)
	}
	if b := load(t, dir); b.Reviewer != "critic" {
		t.Fatalf("build reviewer %q", b.Reviewer)
	}
}

// inReview runs task 1's builder handback, so task 1 is in review, delivered.
func inReview(t *testing.T, f *fakeAdvance) (root, dir string) {
	t.Helper()
	root, dir = buildRepo(t, "check", 0)
	judgeOn(t, dir)
	f.clock = time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC)
	if got := advance(t, f, root, append(reviewArgs("e1", "2026-09-29T12:00:00Z"), "--report", "/r/first.md")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q", got)
	}
	if rc := cmdBuildReviewSent([]string{"--root", root, "--task", "1"}); rc != 0 {
		t.Fatalf("review-sent rc %d", rc)
	}
	f.clock = f.clock.Add(time.Minute)
	f.polled = nil
	return root, dir
}

func TestTheReviewPromptIsTheReviewersAndAsksForAVerdict(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	_, dir := inReview(t, f)
	body, err := os.ReadFile(filepath.Join(dir, "review-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--review --role reviewer --verdict clean|changes", "Do not edit"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("review prompt lacks %q:\n%s", want, body)
		}
	}
}

func TestACleanVerdictFromTheReviewerAcceptsTheTask(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	if got := advance(t, f, root, append(reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean"), "--report", "/r/review.md")...); got != "next 2" {
		t.Fatalf("printed %q", got)
	}
	if len(f.polled) == 0 || f.polled[0] != "reviewer" {
		t.Errorf("advance waited for %v, not the reviewer that handed back", f.polled)
	}
	task := load(t, dir).Tasks[0]
	r := task.Review
	if task.Status != build.StatusDone || r.Reviewed == nil || !*r.Reviewed || r.Result != "clean" || task.ReviewRounds != 1 {
		t.Fatalf("task %+v review %+v", task, r)
	}
	if task.Report != "/r/first.md" || task.ReviewReport != "/r/review.md" || task.Session != "sess-1" {
		t.Fatalf("the reviewer's handback overwrote the builder's record: %+v", task)
	}
	if rec := readJudgeRecord(t, root, "r1"); rec.Reviewed == nil || !*rec.Reviewed || rec.ReviewChangedTree == nil || *rec.ReviewChangedTree {
		t.Fatalf("record %+v", rec)
	}
}

func TestChangesGoBackToTheBuilderAndTheNextHandbackIsReviewedAgain(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	got := advance(t, f, root, append(reviewerArgs("e2", "2026-09-29T12:01:00Z", "changes"), "--report", "/r/review.md")...)
	fields := strings.Fields(got)
	if len(fields) != 3 || fields[0] != "fix" || fields[1] != "1" {
		t.Fatalf("printed %q, want fix 1 MSGFILE", got)
	}
	body, err := os.ReadFile(fields[2])
	if err != nil || !strings.Contains(string(body), "/r/review.md") || strings.Contains(string(body), "--review") {
		t.Fatalf("fix prompt %q: %v", body, err)
	}
	b := load(t, dir)
	task := b.Tasks[0]
	if task.Status != build.StatusSent || b.Current != 1 || b.State != build.StateRunning || task.ReviewRounds != 1 {
		t.Fatalf("build %+v task %+v", b, task)
	}
	if rec := readJudgeRecord(t, root, "r1"); rec.ReviewChangedTree == nil || !*rec.ReviewChangedTree {
		t.Fatalf("a review that asked for changes is recorded as changing nothing: %+v", rec)
	}

	// The builder fixes and hands back: a new round, judged under its own id.
	f.clock = f.clock.Add(time.Minute)
	f.judgeVerdict = Verdict{}
	f.judgeErr = os.ErrDeadlineExceeded
	if got := advance(t, f, root, append(reviewArgs("e3", "2026-09-29T12:02:00Z"), "--report", "/r/fixed.md")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("after the fix printed %q", got)
	}
	if n := len(f.judged); n != 2 || f.judged[1].ID != "b1-t1-r2" {
		t.Fatalf("judged %d, last %+v", n, f.judged[n-1])
	}
	task = load(t, dir).Tasks[0]
	if task.Status != build.StatusReviewing || task.Report != "/r/fixed.md" || task.Review.Result != "" {
		t.Fatalf("task %+v review %+v", task, task.Review)
	}
	if rc := cmdBuildReviewSent([]string{"--root", root, "--task", "1"}); rc != 0 {
		t.Fatal("review-sent")
	}
	f.clock = f.clock.Add(time.Minute)
	if got := advance(t, f, root, reviewerArgs("e4", "2026-09-29T12:03:00Z", "clean")...); got != "next 2" {
		t.Fatalf("round 2 clean printed %q", got)
	}
	if task := load(t, dir).Tasks[0]; task.ReviewRounds != 2 || task.Status != build.StatusDone {
		t.Fatalf("task %+v", task)
	}
}

func TestReviewRoundsStopAtTheCap(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	b := load(t, dir)
	b.Tasks[0].ReviewRounds = build.MaxReviewRounds - 1
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	if got := advance(t, f, root, append(reviewerArgs("e2", "2026-09-29T12:01:00Z", "changes"), "--report", "/r/review.md")...); !strings.HasPrefix(got, "stopped review_rounds ") {
		t.Fatalf("printed %q", got)
	}
	if b := load(t, dir); b.Tasks[0].Status != build.StatusStuck || b.State != build.StateStopped || b.StopTask != 1 {
		t.Fatalf("build %+v", b)
	}
}

func TestAReviewHandbackFromTheBuilderIsRefused(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	args := append(reviewArgs("e2", "2026-09-29T12:01:00Z"), "--review", "--verdict", "clean", "--from", "builder")
	got := advance(t, f, root, args...)
	if !strings.HasPrefix(got, "refused ") || !strings.Contains(got, "builder never reviews") {
		t.Fatalf("printed %q", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing || task.Review.Reviewed != nil || len(f.checked) != 1 {
		t.Fatalf("a builder's review counted: %+v, checks %d", task, len(f.checked))
	}
}

func TestAReviewHandbackInTheBuildersConversationIsRefused(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	// The pane recorded as the reviewer is running the builder's conversation.
	f.sessions = map[string]string{"reviewer": "sess-1"}
	got := advance(t, f, root, reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean")...)
	if !strings.HasPrefix(got, "refused ") || !strings.Contains(got, "conversation") {
		t.Fatalf("printed %q", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing || task.Review.Reviewed != nil {
		t.Fatalf("task %+v", task)
	}
}

func TestAReviewHandbackWithoutAVerdictIsRefused(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	for _, verdict := range []string{"", "objections"} {
		got := advance(t, f, root, reviewerArgs("e2", "2026-09-29T12:01:00Z", verdict)...)
		if !strings.HasPrefix(got, "refused ") || !strings.Contains(got, "clean or changes") {
			t.Fatalf("verdict %q printed %q", verdict, got)
		}
	}
	// Changes with no findings would send the builder nothing to fix.
	if got := advance(t, f, root, reviewerArgs("e2", "2026-09-29T12:01:00Z", "changes")...); !strings.HasPrefix(got, "refused ") || !strings.Contains(got, "no findings report") {
		t.Fatalf("changes without a report printed %q", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing {
		t.Fatalf("task %+v", task)
	}
}

func TestAReviewerThatEditedTheTreeIsNotCounted(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"},
		dirties: []map[string]string{{"src/greet.sh": "h1"}, {"src/greet.sh": "h1"}, {"src/greet.sh": "h2"}}}
	root, dir := inReview(t, f)
	if got := advance(t, f, root, reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean")...); !strings.HasPrefix(got, "stopped review_edited_tree ") {
		t.Fatalf("printed %q", got)
	}
	task := load(t, dir).Tasks[0]
	if task.Status != build.StatusStuck || task.Review.Changed == nil || !*task.Review.Changed || task.Review.Complete() {
		t.Fatalf("task %+v review %+v", task, task.Review)
	}
}

func TestABuilderHandbackDuringReviewIsIgnored(t *testing.T) {
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
	root, dir := inReview(t, f)
	if got := advance(t, f, root, reviewArgs("e2", "2026-09-29T12:01:00Z")...); !strings.HasPrefix(got, "ignored ") {
		t.Fatalf("printed %q", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing || len(f.checked) != 1 {
		t.Fatalf("task %+v checks %d", task, len(f.checked))
	}
}

func TestAReviewHandbackForATaskNotInReviewIsIgnored(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	f := &fakeAdvance{statuses: []string{"idle"}}
	if got := advance(t, f, root, reviewerArgs("e1", "", "clean")...); !strings.HasPrefix(got, "ignored ") {
		t.Fatalf("printed %q", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusSent || len(f.checked) != 0 {
		t.Fatalf("task %+v", task)
	}
}

// A reviewer pane with its own name (HERDR_REVIEWER_NAME) is told that name,
// and its verdict closes the round.
func TestAReviewerWithItsOwnNameClosesTheRound(t *testing.T) {
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	b := load(t, dir)
	b.Reviewer = "critic"
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}, sessions: map[string]string{"critic": "sess-c"}}
	got := advance(t, f, root, reviewArgs("e1", "")...)
	fields := strings.Fields(got)
	if len(fields) != 3 || fields[0] != "review" {
		t.Fatalf("printed %q", got)
	}
	body, err := os.ReadFile(fields[2])
	if err != nil || !strings.Contains(string(body), "--role critic ") || strings.Contains(string(body), "--role reviewer") {
		t.Fatalf("the review prompt does not name the pane it goes to: %q %v", body, err)
	}
	args := append(reviewArgs("e2", ""), "--review", "--verdict", "clean", "--from", "critic")
	if got := advance(t, f, root, args...); got != "next 2" {
		t.Fatalf("critic's verdict printed %q", got)
	}
}

// Findings on a task whose budget is spent are not sent: a fix spends more.
// The task stops unaccepted, as a review that is due over budget does.
func TestChangesOverBudgetStopWithoutAFix(t *testing.T) {
	root, dir := buildRepo(t, "check", 100)
	judgeOn(t, dir)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}, usage: &build.Usage{Input: 50}}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q", got)
	}
	f.usage = &build.Usage{Input: 150}
	if got := advance(t, f, root, append(reviewerArgs("e2", "", "changes"), "--report", "/r/review.md")...); !strings.HasPrefix(got, "stopped budget ") {
		t.Fatalf("printed %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "fix-1.md")); !os.IsNotExist(err) {
		t.Fatalf("a fix prompt was written: %v", err)
	}
	b := load(t, dir)
	if task := b.Tasks[0]; task.Status != build.StatusStuck || b.Current != 1 || b.StopReason != "budget" || task.Review.Complete() {
		t.Fatalf("build %+v task %+v", b, task)
	}
	msg, err := os.ReadFile(filepath.Join(dir, "planner-message.md"))
	if err != nil || !strings.Contains(string(msg), "the fix was not sent") {
		t.Fatalf("planner message %q: %v", msg, err)
	}
}

// noReviewer clears the build's reviewer pane, as in a build started before
// every build needed one.
func noReviewer(t *testing.T, dir string) {
	t.Helper()
	b := load(t, dir)
	b.Reviewer = ""
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
}

// A legacy build with no reviewer pane is stopped where a review round would
// open or close, with reason no_reviewer. Go is the only check: the bridge
// relays the stop, and the planner's message names the remedy.
func TestABuildWithNoReviewerStopsAtItsReviewRound(t *testing.T) {
	assertStopped := func(t *testing.T, got, dir string) {
		t.Helper()
		fields := strings.Fields(got)
		if len(fields) != 4 || fields[0] != "stopped" || fields[1] != "no_reviewer" {
			t.Fatalf("printed %q, want stopped no_reviewer ID MSGFILE", got)
		}
		b := load(t, dir)
		if b.State != build.StateStopped || b.StopReason != "no_reviewer" || b.StopTask != 1 || b.Tasks[0].Status == build.StatusDone {
			t.Fatalf("build %+v task %+v", b, b.Tasks[0])
		}
		msg, err := os.ReadFile(fields[3])
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"records no reviewer pane", "the builder never reviews", "tandem-pair --reviewer KIND", "tandem-handoff"} {
			if !strings.Contains(string(msg), want) {
				t.Errorf("the planner's message lacks %q:\n%s", want, msg)
			}
		}
	}
	t.Run("opening a round", func(t *testing.T) {
		f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
		root, dir := buildRepo(t, "check", 0)
		noReviewer(t, dir)
		f.clock = time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC)
		assertStopped(t, advance(t, f, root, append(reviewArgs("e1", "2026-09-29T12:00:00Z"), "--report", "/r/first.md")...), dir)
		if len(f.judged) != 0 {
			t.Error("a build with no reviewer was judged for a review nobody can do")
		}
	})
	t.Run("closing a round", func(t *testing.T) {
		f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: Verdict{RecordID: "r1"}}
		root, dir := inReview(t, f)
		noReviewer(t, dir)
		assertStopped(t, advance(t, f, root, append(reviewerArgs("e2", "2026-09-29T12:01:00Z", "clean"), "--report", "/r/review.md")...), dir)
	})
}
