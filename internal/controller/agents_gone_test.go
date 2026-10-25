package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
)

// goneObserver is a transport that observes agents by name: gone is the one
// the host says no longer exists, err fails every observation.
type goneObserver struct {
	runner.Transport
	gone string
	err  error
}

func (g goneObserver) ObserveAgent(_ context.Context, name string) (runner.Observation, error) {
	if g.err != nil {
		return runner.Observation{}, g.err
	}
	if name == g.gone {
		return runner.Observation{}, fmt.Errorf("%w: %s", runner.ErrAgentGone, name)
	}
	return runner.Observation{State: runner.ObsRunning}, nil
}

func startedRun(t *testing.T, gone goneObserver) (*cenv, string) {
	t.Helper()
	e := newCEnv(t)
	id := e.start()
	gone.Transport = e.tr
	e.c.d.Transport = gone
	return e, id
}

func TestPauseIfAgentsGonePausesARunWhoseBuilderIsGone(t *testing.T) {
	e, id := startedRun(t, goneObserver{gone: "builder-session"})
	paused, err := e.c.PauseIfAgentsGone(ctx, id)
	if err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	run, _ := e.st.GetRun(ctx, id)
	if run.State != "paused" || run.PauseCode != "agent_exited" {
		t.Fatalf("state %s code %s", run.State, run.PauseCode)
	}
}

func TestPauseIfAgentsGoneLeavesALiveRunAlone(t *testing.T) {
	e, id := startedRun(t, goneObserver{})
	if paused, err := e.c.PauseIfAgentsGone(ctx, id); err != nil || paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	run, _ := e.st.GetRun(ctx, id)
	if run.State != "running" {
		t.Fatalf("state %s", run.State)
	}
}

func TestPauseIfAgentsGoneDoesNotPauseOnAnotherError(t *testing.T) {
	e, id := startedRun(t, goneObserver{err: errors.New("socket hiccup")})
	paused, err := e.c.PauseIfAgentsGone(ctx, id)
	if paused || err != nil {
		t.Fatalf("paused=%v err=%v on an error that is not 'gone'", paused, err)
	}
	if run, _ := e.st.GetRun(ctx, id); run.State != "running" {
		t.Fatalf("state %s", run.State)
	}
}

func TestRecoverResumesARunOnceItsAgentIsBack(t *testing.T) {
	e, id := startedRun(t, goneObserver{gone: "builder-session"})
	if paused, err := e.c.PauseIfAgentsGone(ctx, id); err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	e.c.d.Transport = goneObserver{Transport: e.tr}
	if err := e.c.Recover(ctx, id); err != nil {
		t.Fatal(err)
	}
	if run, _ := e.st.GetRun(ctx, id); run.State != "running" || run.PauseCode != "" {
		t.Fatalf("state %s code %s", run.State, run.PauseCode)
	}
}

func TestRecoverKeepsARunPausedWhileItsAgentIsStillGone(t *testing.T) {
	e, id := startedRun(t, goneObserver{gone: "builder-session"})
	if paused, err := e.c.PauseIfAgentsGone(ctx, id); err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := e.c.Recover(ctx, id); err != nil {
		t.Fatal(err)
	}
	if run, _ := e.st.GetRun(ctx, id); run.State != "paused" || run.PauseCode != "agent_exited" {
		t.Fatalf("state %s code %s", run.State, run.PauseCode)
	}
}

func TestPauseIfAgentsGoneWritesNothingWithoutTheLease(t *testing.T) {
	e, id := startedRun(t, goneObserver{gone: "builder-session"})
	run, _ := e.st.GetRun(ctx, id)
	e.c.setFence(run.CheckoutID, 0)
	if paused, err := e.c.PauseIfAgentsGone(ctx, id); paused || err != nil {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if run, _ := e.st.GetRun(ctx, id); run.State != "running" {
		t.Fatalf("state %s", run.State)
	}
}
