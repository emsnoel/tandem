package main

// tandem state: inspect durable storage and preserve legacy history. status and
// import-legacy --archive do not switch the active build engine; the commands
// in state_migrate.go act on a checkout's history.
//
//	tandem state status --json [--root DIR] [--state-home DIR]
//	tandem state import-legacy --archive PATH [--root DIR] [--state-home DIR]
//
// Each prints exactly one JSON object on stdout and nothing else there;
// diagnostics go to stderr.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const stateUse = "usage: tandem state status --json [--root DIR] [--state-home DIR]\n" +
	"       tandem state import-legacy --archive PATH [--root DIR] [--state-home DIR]\n" +
	"       tandem state active-run --relay DIR\n" + stateMigrateUse

func cmdState(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	switch args[0] {
	case "status":
		return cmdStateStatus(args[1:])
	case "import-legacy":
		return cmdStateImportLegacy(args[1:])
	case "export":
		return cmdStateExport(args[1:])
	case "recover":
		return cmdStateRecover(args[1:])
	case "rebind":
		return cmdStateRebind(args[1:])
	case "gc":
		return cmdStateGC(args[1:])
	case "archive":
		return cmdStateArchive(args[1:])
	case "engine":
		return cmdStateEngine(args[1:])
	case "active-run":
		return cmdStateActiveRun(args[1:])
	}
	fmt.Fprintf(os.Stderr, "tandem state: unknown subcommand %q\n%s\n", args[0], stateUse)
	return exitcode.Usage
}

// stateFlags are shared by both subcommands.
type stateFlags struct {
	root, home string
}

func (f *stateFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.root, "root", "", "a directory inside the checkout (default: the current directory)")
	fs.StringVar(&f.home, "state-home", os.Getenv("TANDEM_STATE_HOME"), "state home, replacing the default base directory")
}

func (f *stateFlags) cwd() (string, error) {
	if f.root != "" {
		return f.root, nil
	}
	return os.Getwd()
}

// statusOut is the one object `state status --json` prints.
type statusOut struct {
	Initialized     bool              `json:"initialized"`
	Engine          string            `json:"engine"`
	CheckoutID      string            `json:"checkout_id,omitempty"`
	StateRoot       string            `json:"state_root,omitempty"`
	SchemaVersion   *int              `json:"schema_version"`
	SupportedSchema int               `json:"supported_schema"`
	Readable        bool              `json:"readable"`
	Diagnostic      string            `json:"diagnostic,omitempty"`
	CurrentRun      *state.RunStatus  `json:"current_run"`
	PausedReason    string            `json:"paused_reason,omitempty"`
	Usage           state.UsageStatus `json:"usage"`
}

func cmdStateStatus(args []string) int {
	fs := flag.NewFlagSet("state status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	asJSON := fs.Bool("json", false, "print the status as one JSON object")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || !*asJSON {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	cwd, err := f.cwd()
	if err != nil {
		return fail(err)
	}
	// A read-only lookup: status must not create the state home, the registry
	// or the database, and must not start recovery.
	id, bound, err := state.LookupIdentity(cwd, f.home)
	if err != nil {
		return fail(err)
	}
	out := statusOut{Engine: "legacy", SupportedSchema: state.LatestSchema(), Usage: state.UsageStatus{Completeness: "none"}}
	if !bound {
		return printJSON(out)
	}
	out.CheckoutID, out.StateRoot = id.CheckoutID, id.StateRoot
	s, err := state.OpenReadOnly(context.Background(), id.StateRoot)
	var newer *state.NewerSchemaError
	switch {
	case errors.Is(err, state.ErrNoStore):
		return printJSON(out)
	case errors.As(err, &newer):
		// Report the version without querying tables whose layout is unknown.
		out.Initialized, out.SchemaVersion, out.Diagnostic = true, &newer.Found, newer.Error()
		if rc := printJSON(out); rc != exitcode.OK {
			return rc
		}
		return exitcode.ManualGate
	case err != nil:
		return fail(err)
	}
	defer s.Close()
	v, err := s.SchemaVersion()
	if err != nil {
		return fail(err)
	}
	run, usage, err := s.Report(context.Background(), id.CheckoutID)
	if err != nil {
		return fail(err)
	}
	out.Initialized, out.Readable, out.SchemaVersion, out.CurrentRun, out.Usage = true, true, &v, run, usage
	if run != nil {
		out.Engine, out.PausedReason = "durable", run.PauseCode
	}
	return printJSON(out)
}

func cmdStateImportLegacy(args []string) int {
	fs := flag.NewFlagSet("state import-legacy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	archive := fs.String("archive", "", "an archive made by `tandem build archive`")
	active := fs.String("active", "", "a directory in a checkout holding a stopped legacy build to carry into a durable run")
	quiescent := fs.Bool("quiescent", false, "with --active: nothing is writing (checked, not trusted)")
	kind := fs.String("builder-kind", "", "with --active: the builder's kind, when the relay's builder.json does not say")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (*archive == "") == (*active == "") {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	if *active != "" {
		return cmdStateImportActive(f, *active, *quiescent, *kind)
	}
	cwd, err := f.cwd()
	if err != nil {
		return fail(err)
	}
	id, err := state.ResolveIdentity(cwd, f.home)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	s, err := state.Open(ctx, id.StateRoot)
	if err != nil {
		return fail(err)
	}
	defer s.Close()
	res, err := s.ImportLegacy(ctx, id, *archive)
	switch {
	case errors.Is(err, workflow.ErrBusy), errors.Is(err, workflow.ErrConflict):
		// Operator action: wait for the build to stop, or retry once the archive is stable.
		fmt.Fprintln(os.Stderr, "tandem state import-legacy:", err)
		return exitcode.ManualGate
	case err != nil:
		return fail(err)
	}
	return printJSON(res)
}
