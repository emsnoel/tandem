package main

// Engine routing for `tandem build`. The relay decides which engine owns a
// command. TANDEM_BUILD_ENGINE only chooses for a start in a relay with no
// run (controller.SelectEngine). Legacy-only commands refuse a durable run.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/build"
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

// routeBuild sends a build subcommand to the durable engine when the relay
// carries a durable run (or, for start, when the environment opts in on a relay
// with no run). It reports whether it handled the command; false leaves it to
// the legacy handlers unchanged.
func routeBuild(sub string, args []string) (int, bool) {
	switch sub {
	case "start", "sent", "review-sent", "advance", "accept", "stop", "status", "tick", "wait", "recover", "review-record", "reconcile", "usage", "budget":
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
		if err := fs.Parse(args); err != nil || fs.NArg() != 0 || f.root == "" {
			fmt.Fprintf(os.Stderr, "usage: tandem build %s --root DIR [--state-home DIR] [--observe-agents]\n", sub)
			return exitcode.Usage, true
		}
	}
	if root == "" {
		return 0, false
	}
	env, repo := "", ""
	if sub == "start" {
		var err error
		if env, repo, err = engineSettings(root); err != nil {
			return fail(err), true
		}
	}
	engine, _, err := controller.SelectEngineWhy(paths.Relay(root), env, repo)
	if err != nil {
		return fail(err), true
	}
	retire := ""
	want := env
	if want == "" {
		want = repo
	}
	if sub == "start" && want == "durable" && engine == controller.EngineLegacy {
		if retire, err = finishedLegacyBuild(root); err != nil {
			return fail(err), true
		}
		engine = controller.EngineDurable
	}
	if engine != controller.EngineDurable {
		if sub == "tick" || sub == "wait" || sub == "recover" || sub == "review-record" || sub == "reconcile" || sub == "usage" || sub == "budget" {
			return fail(fmt.Errorf("no durable run in %s; `tandem build %s` only works on one", paths.Relay(root), sub)), true
		}
		return 0, false
	}
	switch sub {
	case "start":
		return cmdBuildStartDurable(args, retire), true
	case "sent", "review-sent", "advance":
		return legacyOnly(sub, root), true
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
	case "recover":
		return cmdBuildTick(args, true), true
	}
	return 0, false
}

// finishedLegacyBuild returns the build directory of a finished legacy build,
// which a durable start may archive and take the relay from, as a new legacy
// start would. An unfinished build still owns the relay: a durable start is
// refused with what to do instead, never handed to the legacy parser.
func finishedLegacyBuild(root string) (string, error) {
	dir := build.Dir(paths.Relay(root))
	b, err := build.Load(dir)
	if err != nil {
		return "", err
	}
	if b == nil || b.State == build.StateFinished {
		return dir, nil
	}
	return "", fmt.Errorf("an unfinished legacy build (%s) in %s owns this relay, so the durable engine cannot start a run here. "+
		"Continue it on the legacy engine (unset TANDEM_BUILD_ENGINE and build_engine in .tandem.json), or stop it and carry it into a durable run with "+
		"`tandem state import-legacy --active %s --quiescent`", b.State, dir, root)
}

func legacyOnly(sub, root string) int {
	run, _ := controller.ReadActiveRun(paths.Relay(root))
	id := ""
	if run != nil {
		id = " " + run.RunID
	}
	fmt.Fprintf(os.Stderr, "tandem build %s: the run in %s is a durable run%s. `build %s` belongs to the legacy engine and would corrupt it; use `tandem build tick`, `status`, `stop` or `accept --waive`.\n",
		sub, paths.Relay(root), id, sub)
	return exitcode.Usage
}

// openRun is an opened durable run: its store, artifacts and run id.
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
		return nil, fmt.Errorf("no durable run in %s", paths.Relay(root))
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

// engineSettings returns what asks for an engine for a new build in root: the
// environment variable and .tandem.json's build_engine. The variable wins. A
// missing manifest just has no setting.
func engineSettings(root string) (env, repo string, err error) {
	env = os.Getenv("TANDEM_BUILD_ENGINE")
	top, terr := vcs.Toplevel(root)
	if terr != nil {
		top = root
	}
	r, _, err := config.LoadRepo(paths.NearestManifest(root, top))
	if err != nil {
		return "", "", err
	}
	return env, r.BuildEngine, nil
}
