package main

// tandem build budget: set a durable run's input limits.
//
//	tandem build budget [--run ID] [--scope task|run] --limit N [--root DIR] [--state-home DIR]
//
// Limits are in total logical input tokens and are kept on the run, so a restart
// or another shell uses them, not whatever its environment says. 0 removes a
// limit. The scope defaults to run. Raising a limit resumes a run that was paused
// for scheduling, and only the work that is now allowed starts; checks and
// acceptance were never held by a budget.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/vcs"
)

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

func cmdBuildBudget(args []string) int {
	fs := flag.NewFlagSet("build budget", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	runID := fs.String("run", "", "the run, which must be the active run of this checkout; default the active run")
	scope := fs.String("scope", "run", "task or run")
	limit := fs.String("limit", "", "the limit in input tokens; 0 removes it")
	logs := usageSourceFlags(fs)
	usage := func(msg string) int {
		if msg != "" {
			fmt.Fprintln(os.Stderr, "tandem build budget:", msg)
		}
		fmt.Fprintln(os.Stderr, "usage: tandem build budget [--run ID] [--scope task|run] --limit N [--root DIR] [--state-home DIR]")
		return exitcode.Usage
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("")
	}
	if *scope != "task" && *scope != "run" {
		return usage(fmt.Sprintf("--scope is task or run, not %q", *scope))
	}
	if !digitsOnly.MatchString(*limit) {
		return usage("--limit is a whole number of tokens, 0 or more, with no sign or spaces")
	}
	n, err := strconv.ParseInt(*limit, 10, 64)
	if err != nil {
		return usage("--limit is too large")
	}
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return usage("not inside a git checkout; pass --root")
		}
		f.root = top
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	if *runID != "" && *runID != d.run.RunID {
		return fail(fmt.Errorf("run %s is not the active run of this checkout (%s)", *runID, d.run.RunID))
	}
	c, err := controller.New(controller.Dependencies{State: d.st, Artifacts: d.art, Transport: noSend{}, CheckExecutor: controller.NewCheckExecutor(d.art),
		Owner: cliOwner, UsageSource: logs()})
	if err != nil {
		return fail(err)
	}
	dec, err := c.SetBudget(context.Background(), d.run.RunID, *scope, n)
	if err != nil {
		return fail(err)
	}
	run, err := d.st.GetRun(context.Background(), d.run.RunID)
	if err != nil {
		return fail(err)
	}
	return printJSON(map[string]any{"run_id": run.ID, "scope": *scope, "limit": n, "run_state": run.State, "pause_code": run.PauseCode,
		"decision": map[string]any{"state": dec.State, "scheduling_allowed": dec.SchedulingAllowed, "scope": dec.Scope, "reason": dec.Reason}})
}
