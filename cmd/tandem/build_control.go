package main

// tandem build sent, review-sent, stop and status: the legacy build's record
// as the handoff and the bridge move it (see build.go).

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/vcs"
)

func cmdBuildSent(args []string) int {
	fs := flag.NewFlagSet("build sent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	task := fs.Int("task", 0, "the task that was sent")
	session := fs.String("session", "", "the builder session it went to, when known")
	if err := fs.Parse(args); err != nil || *root == "" || *task < 1 {
		fmt.Fprintln(os.Stderr, "usage: tandem build sent --root DIR --task N [--session ID]")
		return exitcode.Usage
	}
	dir, b, err := loadBuild(*root)
	if err != nil {
		return fail(err)
	}
	t := b.Task(*task)
	if t == nil {
		return fail(fmt.Errorf("the build has no task %d", *task))
	}
	t.Status, t.Session, t.SentAt, t.Review = build.StatusSent, *session, time.Now().UTC().Format(time.RFC3339), nil
	if err := b.Save(dir); err != nil {
		return fail(err)
	}
	return exitcode.OK
}

func cmdBuildReviewSent(args []string) int {
	fs := flag.NewFlagSet("build review-sent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	task := fs.Int("task", 0, "the task whose review prompt was delivered")
	if err := fs.Parse(args); err != nil || *root == "" || *task < 1 {
		fmt.Fprintln(os.Stderr, "usage: tandem build review-sent --root DIR --task N")
		return exitcode.Usage
	}
	dir, b, err := loadBuild(*root)
	if err != nil {
		return fail(err)
	}
	t := b.Task(*task)
	if t == nil || t.Status != build.StatusReviewing || t.Review == nil {
		return fail(fmt.Errorf("task %d is not reviewing", *task))
	}
	t.Review.Delivered, t.Review.Pending = true, false
	if err := b.Save(dir); err != nil {
		return fail(err)
	}
	return exitcode.OK
}

// finishOrStop writes the planner's report and message for a build that just
// finished or stopped, and prints the line the bridge acts on.
func finishOrStop(root, dir string, b *build.Build, now time.Time) int {
	// A stopped or finished build no longer protects its conversation, so the next
	// tandem-handoff can reset it. tandem-ask never resets.
	if err := os.Remove(filepath.Join(paths.Relay(root), "build-session")); err != nil && !os.IsNotExist(err) {
		return fail(err)
	}
	id := newID(now)
	reports := filepath.Join(paths.Relay(root), "reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		return fail(err)
	}
	reportPath := filepath.Join(reports, id+".md")
	notes, reviews := reportTexts(b)
	if err := writeFile(reportPath, build.Report(b, notes, reviews)); err != nil {
		return fail(err)
	}
	msgPath := filepath.Join(dir, "planner-message.md")
	if err := writeFile(msgPath, build.PlannerMessage(b, id, reportPath)); err != nil {
		return fail(err)
	}
	if err := b.Save(dir); err != nil {
		return fail(err)
	}
	if b.State == build.StateFinished {
		fmt.Printf("finished %s %s\n", id, msgPath)
	} else {
		fmt.Printf("stopped %s %s %s\n", b.StopReason, id, msgPath)
	}
	return exitcode.OK
}

// writeFile writes then renames, so a reader never sees half a report.
func writeFile(path, body string) error {
	if err := os.WriteFile(path+".tmp", []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func cmdBuildStop(args []string) int {
	fs := flag.NewFlagSet("build stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	reason := fs.String("reason", "handoff", "why the loop stopped")
	detail := fs.String("detail", "", "what went wrong, in a sentence")
	task := fs.Int("task", 0, "task that could not continue")
	if err := fs.Parse(args); err != nil || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem build stop --root DIR [--task N] [--reason handoff] [--detail TEXT]")
		return exitcode.Usage
	}
	dir, b, err := loadBuild(*root)
	if err != nil {
		return fail(err)
	}
	stopTask := *task
	if stopTask == 0 {
		stopTask = b.Current - 1
	}
	b.State, b.StopReason, b.StopDetail, b.StopTask = build.StateStopped, *reason, *detail, stopTask
	if *task != 0 {
		t := b.Task(stopTask)
		if t == nil {
			return fail(fmt.Errorf("the build has no task %d", stopTask))
		}
		t.Status = build.StatusStuck
	}
	return finishOrStop(*root, dir, b, time.Now())
}

func cmdBuildStatus(args []string) int {
	fs := flag.NewFlagSet("build status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "repository root")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	r := *root
	if top, err := vcs.Toplevel(r); err == nil {
		r = top
	}
	_, b, err := loadBuild(r)
	if err != nil {
		return fail(err)
	}
	state := b.State
	if b.State == build.StateStopped {
		state += " (" + b.StopReason + ")"
	}
	fmt.Printf("plan:  %s\nstate: %s, %s\nbudget: %d input tokens per task\n\n",
		b.Plan, state, build.StatusSummary(b), b.Budget)
	fmt.Print(build.Table(b))
	return exitcode.OK
}
