package controller

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// startedBuilder leaves a run whose first task the builder acknowledged, and
// whose first tick after that has seen the pane in this state.
func (e *cenv) startedBuilder(pane string) workflow.Effect {
	e.t.Helper()
	e.start()
	e.tick()
	task := e.tasks()[0]
	eff := e.builderEffect(e.current(task.ID))
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.tr.set(eff.ID, runner.Observation{State: pane, StateSeq: 5})
	e.tick()
	return eff
}

// watch ticks once a minute for d, as a bridge does on its backstop.
func (e *cenv) watch(d time.Duration) {
	e.t.Helper()
	for left := d; left > 0; left -= time.Minute {
		e.clk.advance(time.Minute)
		e.tick()
	}
}

func (e *cenv) wantRun(state, code string) {
	e.t.Helper()
	if st, got := e.runState(); st != state || got != code {
		e.t.Fatalf("the run is %s/%s, want %s/%s", st, got, state, code)
	}
}

func (e *cenv) clockPath() string {
	return filepath.Join(RunDir(e.relay, e.runID), silenceClockFile)
}

// silent leaves a run paused as builder_silent.
func (e *cenv) silent() workflow.Effect {
	e.t.Helper()
	eff := e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow + time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
	return eff
}

func TestSilentBuilderPausesTheRun(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	sends := len(e.tr.sends)
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
	e.watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
	if len(e.tr.sends) != sends {
		t.Fatalf("the pause typed something: %+v", e.tr.sends[sends:])
	}
}

func TestWorkingBuilderIsNotSilent(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsRunning)
	e.watch(2 * SilenceWindow)
	e.wantRun("running", "")
}

func TestBriefIdleDoesNotCount(t *testing.T) {
	e := newCEnv(t)
	eff := e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, StateSeq: 6})
	e.watch(time.Minute)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 7})
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "") // the clock restarted, or this would be well past it
	e.watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
}

// A turn that started and ended between two ticks shows only in the counter.
func TestTurnBetweenTicksRestartsTheClock(t *testing.T) {
	e := newCEnv(t)
	eff := e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 7})
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
}

func TestNewConversationRestartsTheClock(t *testing.T) {
	e := newCEnv(t)
	eff := e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 5, Session: "another"})
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
}

func TestQueuedHandbackBeatsSilence(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	task := e.tasks()[0]
	e.work(1)
	e.queueClaimed(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.watch(2 * time.Minute)
	e.wantRun("running", "")
	if got := e.current(task.ID).Implementation; got != workflow.ImplementationSubmitted {
		t.Fatalf("the handback was not applied: %s", got)
	}
}

// A message that arrives in the queue restarts the clock once. One that stays
// there, because it cannot be applied yet, does not hold the clock off for good.
func TestNewQueuedMessageRestartsTheClockOnce(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	run, cfg, err := e.c.load(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(RunDir(e.relay, run.ID), "q"), "waiting.json", "{}")
	// Ticks would quarantine this file, so the watch is called alone.
	watch := func(d time.Duration) {
		for left := d; left > 0; left -= time.Minute {
			e.clk.advance(time.Minute)
			if err := e.c.watchSilence(ctx, run.ID, cfg); err != nil {
				t.Fatal(err)
			}
		}
	}
	watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
	watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
}

func TestUnreadableClockRestarts(t *testing.T) {
	for name, body := range map[string]string{"corrupt": "{not json", "empty object": "{}"} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			e.startedBuilder(runner.ObsCompleted)
			e.watch(SilenceWindow - time.Minute)
			if err := os.WriteFile(e.clockPath(), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			e.watch(SilenceWindow - time.Minute)
			e.wantRun("running", "")
			e.watch(2 * time.Minute)
			e.wantRun("paused", pauseBuilderSilent)
		})
	}
}

func TestMissingClockRestarts(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	if err := os.Remove(e.clockPath()); err != nil {
		t.Fatal(err)
	}
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
}

func TestPendingPromptIsNotSilent(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	eff := e.builderEffect(e.current(e.tasks()[0].ID))
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted})
	e.watch(5 * time.Minute)
	if _, err := os.Stat(e.clockPath()); !os.IsNotExist(err) {
		t.Fatalf("a prompt nobody acknowledged started the clock: %v", err)
	}
	e.watch(SilenceWindow)
	e.wantRun("paused", pauseWriterUnknown)
}

func TestUnknownPaneIsNotIdle(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsUnknown)
	e.watch(2 * SilenceWindow)
	e.wantRun("running", "")
}

// A host that does not answer says nothing about the pane, and restarts the clock.
func TestUnansweredObservationIsNotIdle(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	e.tr.obsErr = errors.New("herdr did not answer")
	e.watch(time.Minute)
	e.tr.obsErr = nil
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
}

// Nobody watched the pane through a gap as long as the window, as when the
// machine slept, so the gap does not count as silence.
func TestAGapInTheWatchRestartsTheClock(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.clk.advance(SilenceWindow + time.Hour)
	e.tick()
	e.wantRun("running", "")
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
}

// The bridge recovers when it starts, so its restart restarts the clock.
func TestRecoverRestartsTheClock(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.watch(SilenceWindow - time.Minute)
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.watch(SilenceWindow - time.Minute)
	e.wantRun("running", "")
}

func TestRepairAttemptGetsTheSameRule(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.requestChanges(task.ID, 1, workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "fix it"})
	e.tick()
	att := e.current(task.ID)
	if att.Ordinal != 2 {
		t.Fatalf("no repair attempt: %+v", att)
	}
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.tr.set(e.builderEffect(att).ID, runner.Observation{State: runner.ObsCompleted})
	e.tick()
	e.watch(SilenceWindow + time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
}

// A reviewer has its own stall check: a settled builder with a review open is
// not silent.
func TestSubmittedBuilderIsNotSilent(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	att := e.toReview(task.ID, 1)
	e.tr.set(e.builderEffect(att).ID, runner.Observation{State: runner.ObsCompleted})
	e.watch(5 * time.Minute)
	if _, err := os.Stat(e.clockPath()); !os.IsNotExist(err) {
		t.Fatalf("a submitted builder started the clock: %v", err)
	}
}
