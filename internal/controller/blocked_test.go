package controller

import (
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// A builder held at an approval or question dialog. Herdr calls its pane
// blocked, which the transport reports as running and blocked.
var atDialog = runner.Observation{State: runner.ObsRunning, Blocked: true, StateSeq: 5}

// blockedBuilder leaves a run whose started builder sits at a dialog.
func (e *cenv) blockedBuilder() workflow.Effect {
	e.t.Helper()
	eff := e.startedBuilder(runner.ObsRunning)
	e.tr.set(eff.ID, atDialog)
	e.tick()
	return eff
}

// blocked leaves a run paused as builder_blocked.
func (e *cenv) blocked() workflow.Effect {
	e.t.Helper()
	eff := e.blockedBuilder()
	e.watch(SilenceWindow + time.Minute)
	e.wantRun("paused", pauseBuilderBlocked)
	return eff
}

func TestBlockedBuilderPausesTheRun(t *testing.T) {
	e := newCEnv(t)
	e.blockedBuilder()
	sends := len(e.tr.sends)
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
	e.watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderBlocked)
	if len(e.tr.sends) != sends {
		t.Fatalf("the pause typed something: %+v", e.tr.sends[sends:])
	}
}

// Going from the dialog to idle is a status change, so the idle window starts
// from then, even when the host's counter did not move.
func TestBlockedThenIdleRestartsTheClock(t *testing.T) {
	e := newCEnv(t)
	eff := e.blockedBuilder()
	e.watch(SilenceWindow - 2*time.Minute)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: atDialog.StateSeq})
	e.watch(3 * time.Minute)
	e.wantRun("running", "")
	e.watch(SilenceWindow)
	e.wantRun("paused", pauseBuilderSilent)
}

// A new turn at a dialog, such as the next approval, starts the window again.
func TestANewDialogTurnRestartsTheClock(t *testing.T) {
	e := newCEnv(t)
	eff := e.blockedBuilder()
	e.watch(SilenceWindow - 2*time.Minute)
	next := atDialog
	next.StateSeq = 8
	e.tr.set(eff.ID, next)
	e.watch(3 * time.Minute)
	e.wantRun("running", "")
}

func TestRecoverKeepsTheBlockedPauseWhileTheDialogIsUp(t *testing.T) {
	e := newCEnv(t)
	e.blocked()
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.wantRun("paused", pauseBuilderBlocked)
}

// Once the dialog is answered, one recover clears the pause, and the run gets
// a whole window again.
func TestRecoverClearsTheBlockedPauseOnceAnswered(t *testing.T) {
	for name, obs := range map[string]runner.Observation{
		"working":      {State: runner.ObsRunning, StateSeq: 6},
		"idle":         {State: runner.ObsCompleted, StateSeq: 7},
		"same counter": {State: runner.ObsRunning, StateSeq: atDialog.StateSeq},
		"no counters":  {State: runner.ObsRunning},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			eff := e.blocked()
			e.tr.set(eff.ID, obs)
			if err := e.c.Recover(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			e.wantRun("running", "")
			e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 9})
			e.watch(SilenceWindow - time.Minute)
			e.wantRun("running", "")
		})
	}
}

// A pane the host cannot answer for is not known to be past the dialog.
func TestRecoverKeepsTheBlockedPauseForAnUnknownPane(t *testing.T) {
	e := newCEnv(t)
	eff := e.blocked()
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsUnknown})
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.wantRun("paused", pauseBuilderBlocked)
}

// A tick alone never clears it: the owner asks for that with a recover.
func TestATickDoesNotClearTheBlockedPause(t *testing.T) {
	e := newCEnv(t)
	eff := e.blocked()
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, StateSeq: 6})
	e.watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderBlocked)
}

// A handback after the dialog continues the run with no recover. The builder
// answered, worked and settled, as a builder does once it hands back.
func TestLateHandbackContinuesABlockedRun(t *testing.T) {
	e := newCEnv(t)
	eff := e.blocked()
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 7})
	task := e.tasks()[0]
	e.work(1)
	e.queueClaimed(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	e.wantRun("running", "")
	if got := e.current(task.ID).Implementation; got != workflow.ImplementationSubmitted {
		t.Fatalf("the handback was not applied: %s", got)
	}
}
