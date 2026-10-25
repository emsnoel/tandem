package main

// tandem build reconcile: hand the run a tree a person fixed by hand.
//
//	tandem build reconcile --actor NAME --reason TEXT [--root DIR] [--state-home DIR]
//
// The run must be paused by a failure. The edit is snapshotted and counted as
// an intervention, and the whole task is checked and reviewed again on the new
// tree. Nothing is sent to a builder, no repair allowance is created or reset
// and no budget is raised.

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/vcs"
)

func cmdBuildReconcile(args []string) int {
	fs := flag.NewFlagSet("build reconcile", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	actor := fs.String("actor", "", "who made the edit")
	reason := fs.String("reason", "", "what was changed and why")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *actor == "" || *reason == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem build reconcile --actor NAME --reason TEXT [--root DIR] [--state-home DIR]")
		return exitcode.Usage
	}
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return fail(fmt.Errorf("not inside a git checkout; pass --root"))
		}
		f.root = top
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	ctx := context.Background()
	ag, err := controller.RunAgents(ctx, d.st, d.art, d.run.RunID)
	if err != nil {
		return fail(err)
	}
	tr, err := durableTransport(runTransportSpec(ag, f.root, d.art))
	if err != nil {
		return fail(err)
	}
	c, err := d.controller(tr)
	if err != nil {
		return fail(err)
	}
	att, err := c.Reconcile(ctx, d.run.RunID, *actor, *reason)
	if err != nil {
		return fail(err)
	}
	run, err := d.st.GetRun(ctx, d.run.RunID)
	if err != nil {
		return fail(err)
	}
	return printJSON(map[string]any{"run_id": run.ID, "state": run.State, "attempt_id": att.ID, "from_attempt_id": att.ParentAttemptID})
}
