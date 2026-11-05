package main

import (
	"context"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// seedDispatchedRun leaves a run the way an older tandem did: stored with
// builderKind and reviewerKind, with its first task prompt typed and the builder
// having acknowledged it. stopped also records a stop before the upgrade, so the
// run is already stopping.
func seedDispatchedRun(t *testing.T, e *cliEnv, builderKind, reviewerKind string, stopped bool) string {
	t.Helper()
	return seedRun(t, e, builderKind, reviewerKind, stopped, func(st *state.Store, eff workflow.Effect) error {
		return st.AcknowledgeEffect(context.Background(), eff.ID, eff.Nonce)
	})
}

// seedRun starts a run with these kinds and types its first task prompt, then
// lets after decide what became of that prompt. It uses the deliverer directly,
// because a tick now refuses to deliver for a kind with no adapter.
func seedRun(t *testing.T, e *cliEnv, builderKind, reviewerKind string, stopped bool, after func(*state.Store, workflow.Effect) error) string {
	t.Helper()
	ctx := context.Background()
	id, err := state.ResolveIdentity(e.root, e.home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(ctx, id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	c, err := controller.New(controller.Dependencies{State: st, Artifacts: art, Transport: noSend{}, CheckExecutor: controller.NewCheckExecutor(art), Owner: cliOwner})
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.Start(ctx, controller.StartRequest{Root: e.root, PlanPath: e.plan, BuilderKind: builderKind, BuilderSession: "builder",
		PlannerSession: "planner", CheckCommand: "true", ReviewerKind: reviewerKind, ReviewerSession: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	d := &runner.Deliverer{Store: st, Transport: e.tr, Artifacts: art, Owner: cliOwner, RunID: run, AgentKind: builderKind,
		Kinds:  []string{workflow.EffectBuilderDispatch},
		Target: func(workflow.Effect) (string, string) { return builderKind, "builder" }}
	out, err := d.Tick(ctx)
	if err != nil || out.Kind != runner.Sent {
		t.Fatalf("seeding the dispatch: %+v %v", out, err)
	}
	eff, err := st.GetEffect(ctx, out.EffectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := after(st, eff); err != nil {
		t.Fatal(err)
	}
	if stopped {
		if err := c.Stop(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

// upgradedCLI is a checkout on the shipped transport, with Herdr answering from
// get for every pane the closing transport reads.
func upgradedCLI(t *testing.T, get func(string) (*herdr.Agent, error)) *cliEnv {
	t.Helper()
	e := newCLI(t)
	durableTransport = defaultDurableTransport
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	old := closingPanes
	closingPanes = get
	t.Cleanup(func() { closingPanes = old })
	return e
}

func gonePane(name string) (*herdr.Agent, error) {
	return nil, &herdr.Error{Code: "agent_not_found", Message: "agent target " + name + " not found"}
}

// closeRun is what a person does to end the run. It ends archived.
func closeRun(t *testing.T, e *cliEnv, run string) {
	t.Helper()
	for _, step := range [][]string{{"build", "stop"}, {"build", "tick"}, {"state", "recover", "--run", run}, {"state", "archive", "--run", run}} {
		if rc, out, errOut := e.run(append(step, "--root", e.root, "--state-home", e.home)...); rc != exitcode.OK {
			t.Fatalf("%v rc %d %s %s", step, rc, out, errOut)
		}
	}
}

func TestDispatchedDiscontinuedRunCanBeArchived(t *testing.T) {
	for name, tc := range map[string]struct {
		builder, reviewer string
		stopped           bool
		get               func(string) (*herdr.Agent, error)
	}{
		"idle pane":                  {"opencode", "claude", false, newPane("idle").read},
		"gone pane":                  {"opencode", "claude", false, gonePane},
		"mixed run":                  {"codex", "opencode", false, newPane("idle").read},
		"already stopping, idle":     {"opencode", "claude", true, newPane("idle").read},
		"already stopping, gone":     {"opencode", "claude", true, gonePane},
		"mixed and already stopping": {"codex", "opencode", true, newPane("idle").read},
	} {
		t.Run(name, func(t *testing.T) {
			e := upgradedCLI(t, tc.get)
			run := seedDispatchedRun(t, e, tc.builder, tc.reviewer, tc.stopped)
			sent := e.tr.count()

			// The first tick after the upgrade.
			rc, out, errOut := e.sub("tick")
			if rc != exitcode.OK {
				t.Fatalf("tick rc %d %s %s", rc, out, errOut)
			}
			if res := oneJSON(t, out); !tc.stopped && (res["state"] != "paused" || res["pause_code"] != "agent_unsupported") {
				t.Fatalf("tick: %s", out)
			}
			closeRun(t, e, run)
			if got, _ := e.runState(); got != "archived" {
				t.Fatalf("run is %s", got)
			}
			if n := e.tr.count(); n != sent {
				t.Fatalf("%d prompts typed after the upgrade", n-sent)
			}
		})
	}
}

// A pane still working holds the stop, whatever its kind: the writer is not free.
func TestDiscontinuedRunWithAWorkingPaneStaysStopping(t *testing.T) {
	e := upgradedCLI(t, newPane("working").read)
	seedDispatchedRun(t, e, "opencode", "claude", true)
	if rc, out, errOut := e.sub("tick"); rc != exitcode.OK {
		t.Fatalf("tick rc %d %s %s", rc, out, errOut)
	}
	if got, _ := e.runState(); got != "stopping" {
		t.Fatalf("run is %s", got)
	}
}

// A supported builder in a mixed run can come back, so its gone pane is not a
// stopped builder. The stop finishes once the pane returns.
func TestMixedRunKeepsTheStopWhileASupportedBuilderIsGone(t *testing.T) {
	pane := newPane("idle")
	gone := true
	e := upgradedCLI(t, func(name string) (*herdr.Agent, error) {
		if gone {
			return gonePane(name)
		}
		return pane.read(name)
	})
	run := seedDispatchedRun(t, e, "codex", "opencode", true)
	if rc, _, _ := e.sub("tick"); rc == exitcode.OK {
		t.Fatal("a tick that cannot see the builder reported success")
	}
	if got, _ := e.runState(); got != "stopping" {
		t.Fatalf("a gone codex builder freed the stop: run is %s", got)
	}
	gone = false
	closeRun(t, e, run)
	if got, _ := e.runState(); got != "archived" {
		t.Fatalf("run is %s", got)
	}
}

// The closing transport has no way to type: every send is a definite refusal,
// and neither a reset nor a prompt reaches the pane.
func TestClosingTransportTypesNothing(t *testing.T) {
	p := newPane("idle")
	old := closingPanes
	closingPanes = p.read
	defer func() { closingPanes = old }()
	tr := newClosingTransport(controller.Agents{BuilderKind: "opencode", BuilderSession: "builder"})
	for _, kind := range []string{workflow.EffectBuilderDispatch, workflow.EffectCancel} {
		_, err := tr.Send(context.Background(), delivery(kind, "builder"))
		if !runner.IsNotSent(err) {
			t.Errorf("%s: %v, want a definite not-sent", kind, err)
		}
	}
	if len(p.prompts) != 0 || p.resets != 0 {
		t.Fatalf("typed %q with %d resets", p.prompts, p.resets)
	}
	var _ runner.Transport = tr
	if _, ok := any(tr).(runner.PaneReader); ok {
		t.Error("a closing run has no pane review to read")
	}
}

// A verification prompt went to the planner, so the planner's pane answers for
// it, whatever the builder's shows.
func TestClosingTransportObservesAnAgentByName(t *testing.T) {
	old := closingPanes
	closingPanes = func(name string) (*herdr.Agent, error) {
		if name == "planner" {
			return newPane("working").read(name)
		}
		return newPane("idle").read(name)
	}
	defer func() { closingPanes = old }()
	ao, ok := any(newClosingTransport(controller.Agents{BuilderKind: "opencode", BuilderSession: "builder"})).(runner.AgentObserver)
	if !ok {
		t.Fatal("the closing transport cannot observe by name, so a verification prompt is judged by the builder's pane")
	}
	if obs, err := ao.ObserveAgent(context.Background(), "planner"); err != nil || obs.State != runner.ObsRunning {
		t.Fatalf("the planner: %+v %v", obs, err)
	}
}
