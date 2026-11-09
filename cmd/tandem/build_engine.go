package main

// The run commands behind `tandem build`.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/vcs"
)

// cliOwner names the CLI's controller in writer leases. Every `tandem build`
// process is a new process of the same logical controller; the per-run tick
// lock keeps two from ticking at once.
const cliOwner = "tandem-cli"

// durableTransport builds the transport for a run. Tests replace it; the
// default is the Herdr transport (build_transport.go).
var durableTransport = defaultDurableTransport

// noSend is the transport for commands that only read or record: it can never
// deliver anything.
type noSend struct{}

func (noSend) Send(context.Context, runner.Delivery) (runner.Receipt, error) {
	return runner.Receipt{}, runner.NotSent(errors.New("this command does not send prompts"))
}

func (noSend) Observe(context.Context, string) (runner.Observation, error) {
	return runner.Observation{State: runner.ObsUnknown}, nil
}

// peekFlag finds --name VALUE or --name=VALUE without consuming the arguments.
func peekFlag(args []string, name string) string {
	for i, a := range args {
		switch {
		case a == "--"+name && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(a, "--"+name+"="):
			return strings.TrimPrefix(a, "--"+name+"=")
		}
	}
	return ""
}

// runCommand runs a build subcommand that works on the relay's run: every one
// but start needs a run to exist. It reports whether sub is such a command.
func runCommand(sub string, args []string) (int, bool) {
	switch sub {
	case "accept", "stop", "status", "tick", "wait", "recover", "review-record", "reconcile", "usage", "budget":
	default:
		return 0, false
	}
	root := peekFlag(args, "root")
	if root == "" && (sub == "accept" || sub == "review-record" || sub == "reconcile" || sub == "usage" || sub == "budget") {
		if wd, err := os.Getwd(); err == nil {
			if top, err := vcs.Toplevel(wd); err == nil {
				root = top
			}
		}
	}
	if root == "" && sub == "status" {
		root = "."
		if top, err := vcs.Toplevel("."); err == nil {
			root = top
		}
	}
	if sub == "tick" || sub == "recover" {
		// Bad arguments are a usage error whatever the relay holds.
		fs := flag.NewFlagSet("build "+sub, flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		var f durableFlags
		f.register(fs)
		fs.Bool("observe-agents", false, "")
		fs.Bool("fields", false, "")
		if err := fs.Parse(args); err != nil || fs.NArg() != 0 || f.root == "" {
			fmt.Fprintf(os.Stderr, "usage: tandem build %s --root DIR [--state-home DIR] [--observe-agents] [--fields]\n", sub)
			return exitcode.Usage, true
		}
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, buildUse)
		return exitcode.Usage, true
	}
	if run, err := controller.ReadActiveRun(paths.Relay(root)); err != nil {
		return fail(err), true
	} else if run == nil {
		return fail(fmt.Errorf("no run in %s; `tandem build %s` only works on one", paths.Relay(root), sub)), true
	}
	switch sub {
	case "accept":
		return cmdBuildAcceptDurable(args), true
	case "stop":
		return cmdBuildStopDurable(args), true
	case "status":
		return cmdBuildStatusDurable(args), true
	case "review-record":
		return cmdBuildReviewRecord(args), true
	case "reconcile":
		return cmdBuildReconcile(args), true
	case "usage":
		return cmdBuildUsage(args), true
	case "budget":
		return cmdBuildBudget(args), true
	case "tick":
		return cmdBuildTick(args, false), true
	case "wait":
		return cmdBuildWait(args), true
	default: // recover
		return cmdBuildTick(args, true), true
	}
}

// openRun is an opened run: its store, artifacts and run id.
type openRun struct {
	root  string
	run   *controller.ActiveRun
	id    state.Identity
	st    *state.Store
	art   *artifact.Store
	close func()
}

func openDurable(root, home string, readOnly bool) (*openRun, error) {
	run, err := controller.ReadActiveRun(paths.Relay(root))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("no run in %s", paths.Relay(root))
	}
	id, bound, err := state.LookupIdentity(root, home)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, errors.New("no durable state for this checkout; was it started with another --state-home?")
	}
	var st *state.Store
	if readOnly {
		st, err = state.OpenReadOnly(context.Background(), id.StateRoot)
	} else {
		st, err = state.Open(context.Background(), id.StateRoot)
	}
	if err != nil {
		return nil, err
	}
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		st.Close()
		return nil, err
	}
	return &openRun{root: root, run: run, id: id, st: st, art: art, close: func() { st.Close() }}, nil
}

func (d *openRun) controller(tr runner.Transport) (*controller.Controller, error) {
	return controller.New(controller.Dependencies{State: d.st, Artifacts: d.art, Transport: tr,
		CheckExecutor: controller.NewCheckExecutor(d.art), Owner: cliOwner,
		UsageSource: controller.DefaultAgentUsageSource(),
		SessionLogs: controller.DefaultProviderLogs()})
}

type durableFlags struct{ root, home string }

func (f *durableFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.root, "root", "", "repository root")
	fs.StringVar(&f.home, "state-home", os.Getenv("TANDEM_STATE_HOME"), "state home")
}

// checkEngineSettings refuses a setting that asks for anything but the one
// engine: the environment variable or .tandem.json's build_engine.
func checkEngineSettings(root string) error {
	if err := config.CheckEngineSetting("TANDEM_BUILD_ENGINE", os.Getenv("TANDEM_BUILD_ENGINE")); err != nil {
		return err
	}
	top, err := vcs.Toplevel(root)
	if err != nil {
		top = root
	}
	_, _, err = config.LoadRepo(paths.NearestManifest(root, top))
	return err
}
