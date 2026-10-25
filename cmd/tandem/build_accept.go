package main

// tandem build accept: the planner overrules a stop.
//
// A build stops on a red check or a stuck builder. When the planner has read
// the task and judges it done anyway, this records that judgment and its
// reason, so the next tandem-handoff continues from the following task.

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/vcs"
)

func cmdBuildAccept(args []string) int {
	fs := flag.NewFlagSet("build accept", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root (default: this git checkout)")
	task := fs.Int("task", 0, "the stopped task to accept")
	because := fs.String("because", "", "why the planner accepts it")
	if err := fs.Parse(args); err != nil || *task < 1 || strings.TrimSpace(*because) == "" {
		fmt.Fprintln(os.Stderr, `usage: tandem build accept [--root DIR] --task N --because "why"`)
		return exitcode.Usage
	}
	// With no --root, the repository is the current checkout.
	if *root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return fail(fmt.Errorf("not inside a git checkout; pass --root"))
		}
		*root = top
	}
	dir, b, err := loadBuild(*root)
	if err != nil {
		return fail(err)
	}
	t := b.Task(*task)
	switch {
	case b.State != build.StateStopped:
		return refuseAccept("the build is %s, not stopped; there is nothing to accept", b.State)
	case t == nil || *task != b.StopTask:
		return refuseAccept("the build stopped at task %d, not task %d", b.StopTask, *task)
	case t.Status != build.StatusRed && t.Status != build.StatusStuck:
		return refuseAccept("task %d is %s; only a red or stuck task can be accepted", *task, t.Status)
	}
	t.AcceptedFrom, t.Accepted, t.Status = t.Status, strings.TrimSpace(*because), build.StatusDone
	if *task == len(b.Tasks) {
		b.State, b.StopReason, b.StopDetail, b.StopTask = build.StateFinished, "", "", 0
		return finishOrStop(*root, dir, b, time.Now())
	}
	// Still stopped: nothing sends the next task until the planner runs
	// tandem-handoff, which resumes this build at its first task not done.
	b.Current, b.StopReason = *task+1, "accepted"
	b.StopDetail = fmt.Sprintf("the planner accepted task %d", *task)
	if err := b.Save(dir); err != nil {
		return fail(err)
	}
	fmt.Printf("accepted %d; run tandem-handoff %s to send task %d\n", *task, b.Plan, b.Current)
	return exitcode.OK
}

func refuseAccept(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "tandem build accept: "+format+"\n", a...)
	return exitcode.Usage
}
