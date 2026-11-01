package main

// The state subcommands that change or move a checkout's durable history.
//
//	tandem state import-legacy --active DIR --quiescent [--builder-kind K] [--root DIR] [--state-home DIR]
//	tandem state export --run ID --output PATH [--root DIR] [--state-home DIR]
//	tandem state recover --run ID [--root DIR] [--state-home DIR]
//	tandem state rebind --checkout ID --root PATH [--repository-moved] [--state-home DIR]
//	tandem state gc [--root DIR] [--state-home DIR]
//
// Each prints exactly one JSON object on stdout on success. A refusal that
// needs a person (a writer that cannot be ruled out, an existing destination, a
// different repository) exits 23 with the reason on stderr and nothing on
// stdout. Malformed use exits 2.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const stateMigrateUse = "       tandem state import-legacy --active DIR --quiescent [--builder-kind K] [--root DIR] [--state-home DIR]\n" +
	"       tandem state export --run ID --output PATH [--root DIR] [--state-home DIR]\n" +
	"       tandem state recover --run ID [--root DIR] [--state-home DIR]\n" +
	"       tandem state rebind --checkout ID --root PATH [--repository-moved] [--state-home DIR]\n" +
	"       tandem state gc [--root DIR] [--state-home DIR] [--apply]\n" +
	"       tandem state archive --run ID [--root DIR] [--state-home DIR]\n" +
	"       tandem state engine [--root DIR]"

// herdrProbe reports the status of a legacy builder's pane. Tests replace it.
var herdrProbe = func(_ context.Context, pane string) (string, error) {
	a, err := herdr.Get(pane)
	if err != nil {
		return "", err
	}
	if a.Status == nil {
		return "", fmt.Errorf("herdr reports no status for %q", pane)
	}
	return *a.Status, nil
}

// refusal prints why an operator must act and exits 23. Anything else is a
// failure.
func refusal(cmd string, err error) int {
	if errors.Is(err, workflow.ErrBusy) || errors.Is(err, workflow.ErrConflict) {
		fmt.Fprintf(os.Stderr, "tandem state %s: %v\n", cmd, err)
		return exitcode.ManualGate
	}
	fmt.Fprintf(os.Stderr, "tandem state %s: %v\n", cmd, err)
	return exitcode.Internal
}

// openCheckout opens the state of the checkout containing cwd. It never
// creates a binding: a checkout with no state has nothing to act on.
func openCheckout(f stateFlags) (state.Identity, *state.Store, *artifact.Store, error) {
	cwd, err := f.cwd()
	if err != nil {
		return state.Identity{}, nil, nil, err
	}
	id, bound, err := state.LookupIdentity(cwd, f.home)
	if err != nil {
		return id, nil, nil, err
	}
	if !bound {
		return id, nil, nil, errors.New("this checkout has no durable state")
	}
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		return id, nil, nil, err
	}
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		st.Close()
		return id, nil, nil, err
	}
	return id, st, art, nil
}

func quietController(st *state.Store, art *artifact.Store, tr runner.Transport) (*controller.Controller, error) {
	return controller.New(controller.Dependencies{State: st, Artifacts: art, Transport: tr, CheckExecutor: controller.NewCheckExecutor(art), Owner: cliOwner})
}

func cmdStateImportActive(f stateFlags, active string, quiescent bool, kind string) int {
	if !quiescent {
		fmt.Fprintln(os.Stderr, "tandem state import-legacy --active: pass --quiescent to say nothing is writing; it is checked, not trusted")
		return exitcode.Usage
	}
	id, err := state.ResolveIdentity(active, f.home)
	if err != nil {
		return refusal("import-legacy", err)
	}
	ctx := context.Background()
	st, err := state.Open(ctx, id.StateRoot)
	if err != nil {
		return refusal("import-legacy", err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		return refusal("import-legacy", err)
	}
	c, err := quietController(st, art, noSend{})
	if err != nil {
		return refusal("import-legacy", err)
	}
	res, err := c.ImportActive(ctx, controller.ActiveImport{Identity: id, Root: id.CanonicalRoot, Quiescent: quiescent, Probe: herdrProbe, BuilderKind: kind})
	if err != nil {
		return refusal("import-legacy", err)
	}
	return printJSON(res)
}

func cmdStateExport(args []string) int {
	fs := flag.NewFlagSet("state export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	runID := fs.String("run", "", "the run to export")
	out := fs.String("output", "", "a directory that does not exist yet")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *runID == "" || *out == "" {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	_, st, art, err := openCheckout(f)
	if err != nil {
		return refusal("export", err)
	}
	defer st.Close()
	c, err := quietController(st, art, noSend{})
	if err != nil {
		return refusal("export", err)
	}
	m, err := c.Export(context.Background(), *runID, *out)
	if err != nil {
		return refusal("export", err)
	}
	return printJSON(map[string]any{"run_id": m.RunID, "output": *out, "state": m.State, "pause_code": m.PauseCode,
		"artifacts": len(m.Artifacts), "unresolved": m.Unresolved})
}

func cmdStateRecover(args []string) int {
	fs := flag.NewFlagSet("state recover", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	runID := fs.String("run", "", "the run to reconcile")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *runID == "" {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	id, st, art, err := openCheckout(f)
	if err != nil {
		return refusal("recover", err)
	}
	defer st.Close()
	ctx := context.Background()
	run, err := st.GetRun(ctx, *runID)
	if err != nil {
		return refusal("recover", err)
	}
	if run.CheckoutID != id.CheckoutID {
		return refusal("recover", fmt.Errorf("%w: run %s belongs to another checkout", workflow.ErrConflict, *runID))
	}
	ag, err := controller.RunAgents(ctx, st, art, *runID)
	if err != nil {
		return refusal("recover", err)
	}
	tr, err := durableTransport(runTransportSpec(ag, id.CanonicalRoot, art))
	if err != nil {
		return refusal("recover", err)
	}
	c, err := quietController(st, art, tr)
	if err != nil {
		return refusal("recover", err)
	}
	switch rerr := c.Recover(ctx, *runID); {
	case errors.Is(rerr, controller.ErrNotWriter):
		fmt.Fprintln(os.Stderr, "tandem state recover: another controller holds this checkout's writer lease")
		return exitcode.ManualGate
	case rerr != nil:
		// A pause that needs a person is a result, not a failure: the state
		// below says so. Anything else is an error.
		if cur, gerr := st.GetRun(ctx, *runID); gerr != nil || cur.State != "paused" {
			return refusal("recover", rerr)
		}
		fmt.Fprintln(os.Stderr, "tandem state recover:", rerr)
	}
	cur, err := st.GetRun(ctx, *runID)
	if err != nil {
		return refusal("recover", err)
	}
	return printJSON(tickOut{RunID: cur.ID, State: cur.State, PauseCode: cur.PauseCode})
}

func cmdStateRebind(args []string) int {
	fs := flag.NewFlagSet("state rebind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	home := fs.String("state-home", os.Getenv("TANDEM_STATE_HOME"), "state home, replacing the default base directory")
	checkout := fs.String("checkout", "", "the checkout id to move (see `tandem state status --json`)")
	root := fs.String("root", "", "the worktree's new path")
	moved := fs.Bool("repository-moved", false, "the whole repository moved, so its git directory is at a new path")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *checkout == "" || *root == "" {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	id, err := state.Rebind(context.Background(), *home, *checkout, *root, state.RebindOptions{RepositoryMoved: *moved})
	if err != nil {
		return refusal("rebind", err)
	}
	return printJSON(id)
}

// cmdStateGC reports the artifacts nothing refers to; with --apply it deletes
// them. It never deletes by age and never deletes a run.
func cmdStateGC(args []string) int {
	fs := flag.NewFlagSet("state gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	apply := fs.Bool("apply", false, "delete what the report lists (the checkout must be idle)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	cwd, err := f.cwd()
	if err != nil {
		return refusal("gc", err)
	}
	id, bound, err := state.LookupIdentity(cwd, f.home)
	if err != nil {
		return refusal("gc", err)
	}
	if !bound {
		return refusal("gc", errors.New("this checkout has no durable state"))
	}
	var st *state.Store
	if *apply {
		st, err = state.Open(context.Background(), id.StateRoot)
	} else {
		// A report must not create or change anything.
		st, err = state.OpenReadOnly(context.Background(), id.StateRoot)
	}
	if err != nil {
		return refusal("gc", err)
	}
	defer st.Close()
	rep, err := st.GC(context.Background(), *apply)
	if err != nil {
		return refusal("gc", err)
	}
	return printJSON(rep)
}
