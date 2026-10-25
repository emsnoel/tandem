package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/triage"
	"github.com/emsnoel/tandem/internal/vcs"
)

// triageAndMaybeReview opens one review round in the reviewer pane. Only Jev
// triage, when the build has it on, can skip the round, and only when
// calibration and the bridge's explicit skip switch both permit it.
func triageAndMaybeReview(d advanceDeps, root, dir string, b *build.Build, task int, event string) int {
	t := b.Task(task)
	diff, err := d.diff(root, t.Files)
	if err != nil {
		return fail(fmt.Errorf("reading task diff: %w", err))
	}
	brief, err := os.ReadFile(t.Brief)
	if err != nil {
		return fail(fmt.Errorf("reading task brief: %w", err))
	}
	checkOutput := ""
	if raw, err := os.ReadFile(t.CheckLog); err == nil {
		checkOutput = tail(string(raw), 32768)
	}
	// The tree is read once, here: it is the tree the judgment is of and the one
	// a reviewer starts from, so a review can be shown to be of what was judged.
	// The check may write files, and its output belongs in this baseline rather
	// than being counted as a change made during review.
	baseline, baselineErr := d.dirty(root)
	judgedTree := ""
	if baselineErr == nil {
		judgedTree = treeDigest(baseline)
	}
	// Each round is judged under its own id: a round after the builder fixed
	// a reviewer's findings is a new judgment, not a rewrite of the first.
	id := fmt.Sprintf("%s-t%d", b.ID, task)
	if t.ReviewRounds > 0 {
		id = fmt.Sprintf("%s-r%d", id, t.ReviewRounds+1)
	}
	req := judgeRequest{
		TreeHash: judgedTree,
		ID:       id, TaskID: fmt.Sprint(task),
		TaskBrief: string(brief), Diff: diff, CheckCommandExists: b.Check != "",
		CheckPassed: t.CheckExit != nil && *t.CheckExit == 0, CheckOutput: checkOutput,
	}
	// Triage is optional. Without it there is no judgment to weigh, and the
	// reviewer pane reviews: "no judge" means "review".
	var v Verdict
	var judgeErr error
	if b.Judge {
		v, judgeErr = d.judge(root, req)
	}
	verdict, reason, recordID := "review", "", v.RecordID
	if v.WouldSkip {
		verdict = "clean"
	}
	if judgeErr != nil {
		verdict, reason = "unavailable", judgeErr.Error()
	}
	if recordID == "" {
		recordID = req.ID
	}
	facts := triage.SkipFacts{CheckPassed: req.CheckPassed, DiffComplete: d.diffComplete, Truncated: v.State.Truncated}
	if os.Getenv("HERDR_TRIAGE_SKIP") == "1" && judgeErr == nil && v.WouldSkip && v.Answers.ShouldSkip(facts) && !triage.AuditSelected(recordID, triage.PolicyHash()) {
		rows, corrupt, err := loadRows(filepath.Join(root, ".tandem-log"))
		if err == nil && triage.Summarize(rows, triage.CurrentCohort(currentReviewerConfig(root)), corrupt).Passes() {
			t.Review = &build.Review{RecordID: recordID, Verdict: "clean", Reason: "skipped by the triage gate", Skipped: true}
			return exitcode.OK
		}
	}
	if baselineErr != nil {
		return fail(fmt.Errorf("reading the tree after check: %w", baselineErr))
	}
	t.Review = &build.Review{
		RecordID: recordID, Verdict: verdict, Reason: reason,
		SentAt: d.now().UTC().Format(time.RFC3339), FirstEvent: event,
		Tree: baseline,
	}
	t.Status, t.DoneAt = build.StatusReviewing, ""
	if err := b.Save(dir); err != nil {
		return fail(err)
	}
	return printReview(dir, b.Reviewer, task, t)
}

// stopNoReviewer stops a build that reached a review round with no reviewer
// pane: one started before every build needed one. The builder never reviews,
// so no round is opened or closed. This is the only check: the bridge relays
// the stop, and the planner's message carries the remedy.
func stopNoReviewer(d advanceDeps, root, dir string, b *build.Build, task int) int {
	b.State, b.StopReason, b.StopTask = build.StateStopped, "no_reviewer", task
	b.StopDetail = fmt.Sprintf("Task %d is due a review, but this build records no reviewer pane, and the builder never reviews. "+
		"%s. Then run tandem-handoff, which carries on from task %d", task, config.NoReviewerRemedy, task)
	return finishOrStop(root, dir, b, d.now())
}

// advanceReview closes a review round with the reviewer pane's verdict. The
// handback must come from the build's reviewer pane, in a conversation that is
// not the builder's, and carry a verdict. Anything else is refused and the
// round stays open: the builder never reviews.
func advanceReview(d advanceDeps, root, dir string, b *build.Build, task int, report, verdict, from, event, created string) int {
	t := b.Task(task)
	switch {
	case t.Status != build.StatusReviewing || t.Review == nil:
		fmt.Printf("ignored task %d is not in review\n", task)
		return exitcode.OK
	case !t.Review.AcceptsEvent(event, created):
		fmt.Printf("ignored a review handback of task %d from before its review was sent\n", task)
		return exitcode.OK
	case b.Reviewer == "":
		return stopNoReviewer(d, root, dir, b, task)
	case from != b.Reviewer:
		fmt.Printf("refused a review handback of task %d from %q: only the reviewer pane %q reviews, and the builder never reviews\n", task, from, b.Reviewer)
		return exitcode.OK
	case verdict != "clean" && verdict != "changes":
		fmt.Printf("refused a review handback of task %d with verdict %q: it must be clean or changes\n", task, verdict)
		return exitcode.OK
	case verdict == "changes" && report == "":
		fmt.Printf("refused a review handback of task %d that asks for changes with no findings report\n", task)
		return exitcode.OK
	}
	session, settled := settle(d, b.Reviewer)
	if !settled {
		b.State, b.StopReason, b.StopTask = build.StateStopped, "unsettled", task
		b.StopDetail = fmt.Sprintf("%s handed back its review of task %d but did not finish its turn within %s", b.Reviewer, task, settleTimeout)
		return finishOrStop(root, dir, b, d.now())
	}
	if session != "" && session == t.Session {
		fmt.Printf("refused a review handback of task %d: pane %q is running the builder's conversation (%s), and the builder never reviews\n", task, b.Reviewer, session)
		return exitcode.OK
	}
	after, err := d.dirty(root)
	if err != nil {
		return fail(fmt.Errorf("reading the tree: %w", err))
	}
	// The builder's conversation is what the task budget measures; read it
	// again, since the planner may have used it during the review.
	t.Usage = d.usage(t.Session)
	if report != "" {
		t.ReviewReport = report
	}
	edited, err := finishReview(d, root, dir, task, b.Check, verdict, t, after)
	if err != nil {
		return fail(err)
	}
	if edited {
		b.StopDetail = fmt.Sprintf("%s changed task %d's files while it reviewed", b.Reviewer, task)
	}
	out := build.DecideAdvance(build.AdvanceFacts{
		CheckExit: t.CheckExit, Usage: t.Usage, Budget: b.Budget, IsLast: task == len(b.Tasks),
		ReviewComplete: t.Review.Complete(),
		ReviewVerdict:  verdict, ReviewRounds: t.ReviewRounds, ReviewEditedTree: edited,
	})
	return applyOutcome(d, root, dir, b, task, event, out)
}

// pauseReview records a review that is due but cannot start because the task's
// budget is spent. Nothing is sent and Jev is not asked: both would spend more
// on a task that is over its limit.
func pauseReview(d advanceDeps, root, dir string, b *build.Build, task int, event string) error {
	t := b.Task(task)
	baseline, err := d.dirty(root)
	if err != nil {
		return fmt.Errorf("reading the tree after check: %w", err)
	}
	t.Review = &build.Review{
		RecordID: fmt.Sprintf("%s-t%d", b.ID, task), Verdict: "review", Reason: "paused: task budget spent",
		FirstEvent: event, Pending: true, Tree: baseline,
	}
	t.DoneAt = ""
	return nil
}

// finishReview records the reviewer pane's verdict, measured before another
// check writes anything. It reports whether the reviewer edited the tree: a
// reviewer reads, so a round that changed the task's files or added one is not
// counted, whatever its verdict.
func finishReview(d advanceDeps, root, dir string, task int, command, verdict string, t *build.Task, after map[string]string) (bool, error) {
	changed, other := t.Review.ChangedTree(after, t.Files)
	reviewed := true
	t.Review.Changed, t.Review.Reviewed, t.Review.OtherPaths, t.Review.Result = &changed, &reviewed, other, verdict
	t.ReviewRounds++
	// changed and other only attribute the edit. The check ran against the whole
	// tree, so any content change at all, including a file that was already
	// dirty and is not this task's, makes its result stale.
	stale := len(vcs.Changed(t.Review.Tree, after)) > 0
	// The calibration record asks whether the review changed the outcome: a
	// request for changes, or an edit that voids the round. Records are best
	// effort, as in judgeTask.
	outcome := verdict == "changes" || changed
	_ = updateJudgeRecord(filepath.Join(root, ".tandem-log"), t.Review.RecordID, func(rec *judgeRecord) {
		rec.ReviewChangedTree, rec.Reviewed = &outcome, &reviewed
		completeEvidence(rec, currentReviewerConfig(root), treeDigest(t.Review.Tree))
	})
	if stale {
		return changed, checkTask(d, root, dir, task, command, t)
	}
	return changed, nil
}

func checkTask(d advanceDeps, root, dir string, task int, command string, t *build.Task) error {
	if command == "" {
		t.CheckExit = nil
		return nil
	}
	t.CheckLog = filepath.Join(dir, "checks", fmt.Sprintf("task-%d.log", task))
	code, err := d.runCheck(root, command, t.CheckLog)
	if err != nil {
		return fmt.Errorf("running the check: %w", err)
	}
	t.CheckExit = &code
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func printReview(dir, reviewer string, task int, t *build.Task) int {
	path, err := writeReviewPrompt(dir, reviewer, task, t)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("review %d %s\n", task, path)
	return exitcode.OK
}

// writeReviewPrompt stores the review prompt for a task and returns its path.
func writeReviewPrompt(dir, reviewer string, task int, t *build.Task) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("review-%d.md", task))
	body := build.ReviewPrompt(task, t.Title, t.Brief, reviewer)
	return path, writeFile(path, body)
}

// printFix stores the builder's fix prompt for a task and prints the line the
// bridge acts on: `fix TASK MSGFILE`.
func printFix(dir string, task int, t *build.Task) int {
	path := filepath.Join(dir, fmt.Sprintf("fix-%d.md", task))
	if err := writeFile(path, build.FixPrompt(task, t.Title, t.ReviewReport)); err != nil {
		return fail(err)
	}
	fmt.Printf("fix %d %s\n", task, path)
	return exitcode.OK
}
