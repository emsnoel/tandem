package controller

import (
	"os"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// How a run paused as builder_silent continues. The restart route is proved by
// TestEveryPauseHasAWayOut.

func TestRecoverKeepsTheSilentPauseWhileNothingMoved(t *testing.T) {
	e := newCEnv(t)
	e.silent()
	e.clk.advance(time.Hour)
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.wantRun("paused", pauseBuilderSilent)
}

func TestRecoverClearsTheSilentPauseOnceTheTurnMovedOn(t *testing.T) {
	for name, obs := range map[string]runner.Observation{
		"working again":        {State: runner.ObsRunning, StateSeq: 6},
		"a whole turn since":   {State: runner.ObsCompleted, StateSeq: 7},
		"a new conversation":   {State: runner.ObsCompleted, StateSeq: 5, Session: "another"},
		"working, no counters": {State: runner.ObsRunning},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			eff := e.silent()
			e.tr.set(eff.ID, obs)
			if err := e.c.Recover(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			e.wantRun("running", "")
			// It gets a whole window again, and the rule still holds after it.
			e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 9})
			e.watch(SilenceWindow - time.Minute)
			e.wantRun("running", "")
			e.watch(2 * time.Minute)
			e.wantRun("paused", pauseBuilderSilent)
		})
	}
}

// With no clock there is nothing to say the turn moved, so an idle pane stays
// paused and only a pane seen working clears it.
func TestRecoverWithNoClockNeedsAWorkingPane(t *testing.T) {
	e := newCEnv(t)
	eff := e.silent()
	if err := os.Remove(e.clockPath()); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.wantRun("paused", pauseBuilderSilent)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, StateSeq: 6})
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.wantRun("running", "")
}

// A tick alone never clears the pause for a pane that merely moved.
func TestATickDoesNotClearTheSilentPause(t *testing.T) {
	e := newCEnv(t)
	eff := e.silent()
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, StateSeq: 6})
	e.watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
}

// The note's other way on: a late handback is applied and the run goes on by
// itself, with no recover.
func TestLateHandbackContinuesASilentRun(t *testing.T) {
	e := newCEnv(t)
	e.silent()
	task := e.tasks()[0]
	e.work(1)
	e.queueClaimed(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	e.wantRun("running", "")
	if got := e.current(task.ID).Implementation; got != workflow.ImplementationSubmitted {
		t.Fatalf("the handback was not applied: %s", got)
	}
	if e.delivered()[effectReviewPrompt] != 1 {
		e.tick()
	}
	if got := e.delivered()[effectReviewPrompt]; got != 1 {
		t.Fatalf("the run did not go on to its review: %d prompts", got)
	}
}

// A report that it cannot finish moves the run from one pause to the other.
func TestLateStuckReplacesTheSilentPause(t *testing.T) {
	e := newCEnv(t)
	e.silent()
	e.queueClaimed(e.tasks()[0].ID, workflow.EventBuilderStuck, "{\"message\":\"no\"}\n")
	e.tick()
	e.wantRun("paused", workflow.PauseStuck)
}
