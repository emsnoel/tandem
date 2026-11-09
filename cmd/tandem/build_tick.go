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

// cmdBuildTick runs one tick (or a recovery) of the active run. The
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
	asFields := fs.Bool("fields", false, "print NUL-separated fields for bash (run_id, state, pause_code, skipped) in place of JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || f.root == "" {
		fmt.Fprintf(os.Stderr, "usage: tandem build %s --root DIR [--state-home DIR] [--observe-agents] [--fields]\n", verb)
		return exitcode.Usage
	}
	out, code, err := tickRun(f, recoverRun, *observe)
	switch {
	case err != nil && out == nil && code == 0:
		return fail(err)
	case err != nil:
		fmt.Fprintln(os.Stderr, "tandem build:", err)
	}
	if out != nil && *asFields {
		writeFields(os.Stdout, out.RunID, out.State, out.PauseCode, out.Skipped)
	} else if out != nil {
		if rc := printJSON(*out); rc != exitcode.OK {
			return rc
		}
	}
	return code
}

// tickRun is one tick (or recover) of the active run, without printing.
// It returns what a tick reports, an exit code, and an error. A zero code with
// an error is a plain failure; ManualGate with an error is a run that needs a
// person, and the error says why. A tick that fails still reports the run when
// it can read it, so a pause made in the same pass reaches the planner.
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
	err = tickOnce(ctx, d, f.root, recoverRun, observe)
	if errors.Is(err, controller.ErrNotWriter) {
		return nil, exitcode.ManualGate, errors.New("another controller holds this checkout's writer lease; `tandem build recover` reconciles it")
	}
	run, gerr := d.st.GetRun(ctx, runID)
	if gerr != nil {
		return nil, 0, errors.Join(err, gerr)
	}
	out := &tickOut{RunID: runID, State: run.State, PauseCode: run.PauseCode}
	switch {
	case err == nil:
		return out, exitcode.OK, nil
	case (run.State == "paused" && gateCodes[run.PauseCode]) || errors.Is(err, controller.ErrPlanChanged):
		// The run is paused, not failed: say so with the machine code, and ask
		// for a person.
		return out, exitcode.ManualGate, err
	}
	return out, exitcode.Internal, err
}

// tickOnce builds the run's controller and gives it one tick or recover. The
// gone-pane watch runs even when the tick failed: a failure that repeats on
// every pass must not hide a pane that is gone. It writes only under the writer
// lease the tick renewed.
func tickOnce(ctx context.Context, d *openRun, root string, recoverRun, observe bool) error {
	runID := d.run.RunID
	ag, err := controller.RunAgents(ctx, d.st, d.art, runID)
	if err != nil {
		return err
	}
	tr, err := runTransport(ag, root, d.art)
	if err != nil {
		return err
	}
	c, err := d.controller(tr)
	if err != nil {
		return err
	}
	if recoverRun {
		err = c.Recover(ctx, runID)
	} else {
		err = c.Tick(ctx, runID)
	}
	if observe && !errors.Is(err, controller.ErrNotWriter) {
		_, oerr := c.PauseIfAgentsGone(ctx, runID)
		err = errors.Join(err, oerr)
	}
	return err
}
