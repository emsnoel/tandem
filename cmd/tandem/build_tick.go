package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

type tickOut struct {
	RunID     string `json:"run_id"`
	State     string `json:"state,omitempty"`
	PauseCode string `json:"pause_code,omitempty"`
	Skipped   string `json:"skipped,omitempty"`
}

// cmdBuildTick runs one tick (or a recovery) of the active durable run. The
// bridge calls it; it is not a loop. Another tick of the same run in progress
// makes this one a successful no-op.
func cmdBuildTick(args []string, recoverRun bool) int {
	verb := "tick"
	if recoverRun {
		verb = "recover"
	}
	fs := flag.NewFlagSet("build "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	observe := fs.Bool("observe-agents", false, "after the tick, pause the run if its builder or reviewer is gone")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || f.root == "" {
		fmt.Fprintf(os.Stderr, "usage: tandem build %s --root DIR [--state-home DIR] [--observe-agents]\n", verb)
		return exitcode.Usage
	}
	out, code, err := tickRun(f, recoverRun, *observe)
	switch {
	case err != nil && code == 0:
		return fail(err)
	case err != nil:
		fmt.Fprintln(os.Stderr, "tandem build:", err)
	}
	if out != nil {
		if rc := printJSON(*out); rc != exitcode.OK {
			return rc
		}
	}
	return code
}

// tickRun is one tick (or recover) of the active durable run, without printing.
// It returns what a tick reports, an exit code, and an error. A zero code with
// an error is a plain failure; ManualGate with an error is a run that needs a
// person, and the error says why.
func tickRun(f durableFlags, recoverRun, observe bool) (*tickOut, int, error) {
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return nil, 0, err
	}
	defer d.close()
	runID := d.run.RunID

	lock, err := state.OpenLockerNamed(d.id.StateRoot, "tick-"+runID)
	if err != nil {
		return nil, 0, err
	}
	unlock, err := lock.TryLockExclusive()
	if errors.Is(err, workflow.ErrBusy) {
		return &tickOut{RunID: runID, Skipped: "busy"}, exitcode.OK, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer unlock()

	ctx := context.Background()
	ag, err := controller.RunAgents(ctx, d.st, d.art, runID)
	if err != nil {
		return nil, 0, err
	}
	tr, err := durableTransport(runTransportSpec(ag, f.root, d.art))
	if err != nil {
		return nil, 0, err
	}
	c, err := d.controller(tr)
	if err != nil {
		return nil, 0, err
	}
	if recoverRun {
		err = c.Recover(ctx, runID)
	} else {
		err = c.Tick(ctx, runID)
	}
	if observe && err == nil {
		_, err = c.PauseIfAgentsGone(ctx, runID)
	}
	if code, ok := gatedRun(ctx, d.st, runID); err != nil && (ok || errors.Is(err, controller.ErrPlanChanged)) {
		// The run is paused, not failed: say so in the one JSON object, with the
		// machine code, and ask for a person.
		var out *tickOut
		if run, gerr := d.st.GetRun(ctx, runID); gerr == nil {
			code = run.PauseCode
			out = &tickOut{RunID: runID, State: run.State, PauseCode: code}
		}
		return out, exitcode.ManualGate, err
	}
	if errors.Is(err, controller.ErrNotWriter) {
		return nil, exitcode.ManualGate, errors.New("another controller holds this checkout's writer lease; `tandem build recover` reconciles it")
	}
	if err != nil {
		return nil, 0, err
	}
	run, err := d.st.GetRun(ctx, runID)
	if err != nil {
		return nil, 0, err
	}
	return &tickOut{RunID: runID, State: run.State, PauseCode: run.PauseCode}, exitcode.OK, nil
}
