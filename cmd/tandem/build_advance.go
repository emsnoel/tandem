package main

// tandem build advance: what happens when the builder hands a task back, or
// the reviewer pane hands back its verdict on one.
//
// It runs in the relay bridge, outside the builder's sandbox, after a pane has
// queued `tandem-handback --task N` (with --review for the reviewer). It waits
// for that pane to finish its turn, measures the task (files, tokens, the
// check), and decides whether the bridge sends a review, the reviewer's
// findings, the next task, finishes, or stops.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/check"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/vcs"
)

const (
	// settleTimeout: the builder has just queued its handback, so its turn
	// ends within seconds. Ten minutes also covers a permission dialog a
	// human clears.
	settleTimeout = 10 * time.Minute
	// settleSlice bounds one watch. A broken stream sleeps out its slice, so
	// slicing the wait lets a fresh watch open after an error.
	settleSlice = 30 * time.Second
	// checkTimeout stops a hung check. A real one takes minutes.
	checkTimeout = 30 * time.Minute
)

type advanceDeps struct {
	get      func(name string) (*herdr.Agent, error)
	sleep    func(time.Duration)
	watch    func(names ...string) (eventWatch, error)
	now      func() time.Time
	dirty    func(root string) (map[string]string, error)
	usage    func(session string) *build.Usage
	runCheck func(root, command, logPath string) (exit int, err error)
	judge    func(root string, req judgeRequest) (Verdict, error)
	diff     func(root string, files []string) (string, error)
	// diffComplete says whether diff returns exactly the attempt's complete
	// change. The legacy HEAD-relative diff of a shared worktree cannot, so the
	// live value is false and a skip never acts on it.
	diffComplete bool
}

var liveAdvanceDeps = advanceDeps{
	get:   herdr.Get,
	sleep: time.Sleep,
	watch: liveWatch,
	now:   time.Now,
	dirty: vcs.DirtyHashes,
	usage: codexUsage,
	judge: func(root string, req judgeRequest) (Verdict, error) {
		ctx, cancel := context.WithTimeout(context.Background(), judgeTimeout)
		defer cancel()
		return judgeTask(ctx, newJevClient(apiKey(), judgeTimeout), req, root)
	},
	diff: vcs.TaskDiff,
	// Never claim completeness for a legacy diff; durable snapshots can.
	diffComplete: false,
	runCheck: func(root, command, logPath string) (int, error) {
		r, _, err := runCheckLogged(root, command, nil, logPath, checkTimeout)
		return r.Exit, err
	},
}

// runCheckLogged runs a check with a deadline and returns its result and log.
func runCheckLogged(root, command string, extra []string, logPath string, timeout time.Duration) (check.Result, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := check.Run(ctx, root, command, extra, logPath)
	if err != nil {
		return r, "", err
	}
	text, _ := os.ReadFile(logPath)
	return r, string(text), nil
}

func summary(r check.Result, text string) string { return check.Summary(r, text) }

// codexUsage reads what a Codex session cost from its rollout log, including
// subagent logs. nil when the session is not Codex or the log cannot be read;
// the budget treats unmeasured as within it.
func codexUsage(session string) *build.Usage {
	path, err := findCodexRollout(filepath.Join(paths.CodexHome(), "sessions"), session)
	if err != nil || path == "" {
		return nil
	}
	var all []rollout.Phase
	children := findCodexChildren(filepath.Join(paths.CodexHome(), "sessions"), path, session)
	for i, p := range append([]string{path}, children...) {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		ps, err := rollout.Parse(f, filepath.Base(p))
		f.Close()
		if err != nil && i == 0 {
			return nil
		}
		all = append(all, ps...)
	}
	// First is the task conversation's own first call; subagents add their
	// calls and input, because they spend the same allowance.
	t := rollout.Total(all)
	return &build.Usage{Calls: t.Calls, Input: t.Input, First: t.FirstContext, Peak: t.Peak, Subagents: len(children)}
}

func cmdBuildAdvance(d advanceDeps, args []string) int {
	fs := flag.NewFlagSet("build advance", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	task := fs.Int("task", 0, "the task handed back")
	report := fs.String("report", "", "the report handed back with it")
	stuck := fs.Bool("stuck", false, "the builder said it could not finish")
	review := fs.Bool("review", false, "the reviewer's verdict on the task, not the builder's handback")
	verdict := fs.String("verdict", "", "with --review: clean or changes")
	from := fs.String("from", "", "with --review: the pane that handed it back")
	event := fs.String("event", "", "mailbox event id")
	created := fs.String("created", "", "mailbox event creation time")
	if err := fs.Parse(args); err != nil || *root == "" || *task < 1 {
		fmt.Fprintln(os.Stderr, "usage: tandem build advance --root DIR --task N [--report FILE] [--stuck] [--review --verdict clean|changes --from PANE]")
		return exitcode.Usage
	}
	dir, b, err := loadBuild(*root)
	if err != nil {
		return fail(err)
	}
	// A repeated or late event must never move the loop: sending task N+1
	// twice would give two conversations the same work.
	switch t := b.Task(*task); {
	case b.State == build.StateFinished:
		fmt.Println("ignored the build is finished")
		return exitcode.OK
	case *task != b.Current:
		fmt.Printf("ignored task %d is not the current task (%d)\n", *task, b.Current)
		return exitcode.OK
	case t == nil || t.Status == build.StatusDone:
		fmt.Printf("ignored task %d is already done\n", *task)
		return exitcode.OK
	}
	t := b.Task(*task)
	if t.Status == build.StatusReviewing && t.Review != nil && t.Review.Pending {
		// The review is waiting for budget. Only `tandem-handoff` after a
		// budget increase sends it; an old event must not.
		fmt.Printf("ignored task %d has a review waiting for budget\n", *task)
		return exitcode.OK
	}
	if *review {
		return advanceReview(d, *root, dir, b, *task, *report, *verdict, *from, *event, *created)
	}
	if t.Status == build.StatusReviewing && t.Review != nil {
		if !t.Review.AcceptsEvent(*event, *created) {
			if *event == t.Review.FirstEvent && !t.Review.Delivered {
				if b.Reviewer == "" {
					return stopNoReviewer(d, *root, dir, b, *task)
				}
				return printReview(dir, b.Reviewer, *task, t)
			}
			fmt.Println("ignored a repeat of the first handback")
			return exitcode.OK
		}
		// Only the reviewer's verdict closes a review round. The builder is
		// sent the findings when there are any.
		fmt.Printf("ignored task %d is in review; only %s's verdict closes the round\n", *task, b.Reviewer)
		return exitcode.OK
	}

	session, settled := settle(d, b.Builder)
	if !settled {
		b.State, b.StopReason, b.StopTask = build.StateStopped, "unsettled", *task
		b.StopDetail = fmt.Sprintf("%s handed back task %d but did not finish its turn within %s", b.Builder, *task, settleTimeout)
		return finishOrStop(*root, dir, b, d.now())
	}
	if session != "" {
		t.Session = session
	}

	after, err := d.dirty(*root)
	if err != nil {
		return fail(fmt.Errorf("reading the tree: %w", err))
	}
	t.Files = vcs.Changed(t.TreeBefore, after)
	t.Usage = d.usage(t.Session)
	if *report != "" {
		t.Report = *report
	}
	if err := checkTask(d, *root, dir, *task, b.Check, t); err != nil {
		return fail(err)
	}
	facts := build.AdvanceFacts{
		Stuck: *stuck, CheckExit: t.CheckExit, Usage: t.Usage, Budget: b.Budget, IsLast: *task == len(b.Tasks),
		ReviewComplete: t.Review.Complete(), ReviewRounds: t.ReviewRounds,
	}
	if b.Budget > 0 && t.Usage == nil {
		fmt.Fprintf(os.Stderr, "tandem build: task %d usage was not measured; its budget of %d was not enforced\n", *task, b.Budget)
	}
	out := build.DecideAdvance(facts)
	if out.Next == "review" {
		if b.Reviewer == "" {
			return stopNoReviewer(d, *root, dir, b, *task)
		}
		// Status stays as it was: triageAndMaybeReview marks the task reviewing
		// when it opens a round, and leaves it alone when the gate skips one.
		if rc := triageAndMaybeReview(d, *root, dir, b, *task, *event); rc != exitcode.OK || t.Status == build.StatusReviewing {
			return rc
		}
		facts.ReviewComplete = true
		out = build.DecideAdvance(facts)
	}
	return applyOutcome(d, *root, dir, b, *task, *event, out)
}

// settle waits for the named pane to finish its turn and returns its session.
func settle(d advanceDeps, name string) (string, bool) {
	deadline := d.now().Add(settleTimeout)
	for d.now().Before(deadline) {
		slice := min(settleSlice, deadline.Sub(d.now()))
		w, err := d.watch(name)
		if err != nil {
			d.sleep(slice)
			continue
		}
		session, settled := "", false
		if a := w.Snapshot(name); a != nil && agent.Settled(a.StatusString()) {
			session, settled = a.SessionID(), true
		} else {
			matched, read, _ := awaitStatus(d.get, d.now, d.sleep, w, slice, agent.Settled, name)
			if matched {
				if read == nil {
					read, _ = d.get(name)
				}
				if read != nil {
					session, settled = read.SessionID(), true
				}
			}
		}
		w.Close()
		if settled {
			return session, true
		}
	}
	return "", false
}

// applyOutcome records what DecideAdvance decided for task and prints the line
// the bridge acts on.
func applyOutcome(d advanceDeps, root, dir string, b *build.Build, task int, event string, out build.Outcome) int {
	t := b.Task(task)
	t.Status = out.Status
	if out.Status == build.StatusDone {
		t.DoneAt = d.now().UTC().Format(time.RFC3339)
	}
	if out.Status == build.StatusReviewing {
		// Spending stopped the loop before the review ran. The task is not
		// done; a handoff after a budget increase sends the review.
		if err := pauseReview(d, root, dir, b, task, event); err != nil {
			return fail(err)
		}
	}
	switch out.Next {
	case "fix":
		// The findings go to the builder conversation that built the task. It
		// hands back as for the task itself, and that opens the next round.
		if err := b.Save(dir); err != nil {
			return fail(err)
		}
		return printFix(dir, task, t)
	case "next":
		b.Current, b.State = task+1, build.StateRunning
		// The ledger now carries what later tasks need from this
		// conversation, so it no longer needs protecting from a reset.
		if err := os.Remove(filepath.Join(paths.Relay(root), "build-session")); err != nil && !os.IsNotExist(err) {
			return fail(err)
		}
		if err := b.Save(dir); err != nil {
			return fail(err)
		}
		fmt.Printf("next %d\n", b.Current)
		return exitcode.OK
	case "finish":
		b.State = build.StateFinished
	default:
		b.State, b.StopReason, b.StopTask = build.StateStopped, out.Reason, task
		if out.Status == build.StatusDone {
			b.Current = task + 1
		}
	}
	return finishOrStop(root, dir, b, d.now())
}
