package controller

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// remindersOff makes the remind window as long as the silence window, which
// turns the reminder off, for a test of the silence clock alone.
func remindersOff(t *testing.T) {
	t.Helper()
	old := RemindWindow
	RemindWindow = SilenceWindow
	t.Cleanup(func() { RemindWindow = old })
}

// reminders are the reminders typed so far.
func (e *cenv) reminders() []runner.Delivery {
	e.t.Helper()
	var out []runner.Delivery
	for _, d := range e.tr.sends {
		if d.Kind == effectBuilderReminder {
			out = append(out, d)
		}
	}
	return out
}

func (e *cenv) textOf(d runner.Delivery) string {
	e.t.Helper()
	raw, err := e.art.ReadAll(ctx, d.PromptHash, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

// The builder ended its turn without handing back (#90). Tandem reminds it
// once within the remind window, and tells the planner when it is still idle a
// remind window after that.
func TestIdleBuilderIsRemindedThenThePlannerIsTold(t *testing.T) {
	e := newCEnv(t)
	eff := e.startedBuilder(runner.ObsCompleted)
	e.watch(RemindWindow - time.Minute)
	if got := e.reminders(); len(got) != 0 {
		t.Fatalf("reminded inside the window: %+v", got)
	}
	e.watch(time.Minute)
	got := e.reminders()
	if len(got) != 1 {
		t.Fatalf("want one reminder after %s idle, got %d", RemindWindow, len(got))
	}
	if got[0].SessionID != e.cfg().BuilderSession || got[0].AttemptID != eff.AttemptID {
		t.Fatalf("the reminder went to %q for %s", got[0].SessionID, got[0].AttemptID)
	}
	text := e.textOf(got[0])
	for _, want := range []string{
		"--attempt " + eff.AttemptID,
		"--effect " + eff.ID,
		"--report <file>",
		"in the foreground",
		"file-write tool",
		"cat",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the reminder is missing %q:\n%s", want, text)
		}
	}
	e.wantRun("running", "")
	// The builder answers the reminder and goes idle again with nothing handed back.
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 8})
	e.watch(RemindWindow - time.Minute)
	e.wantRun("running", "")
	e.watch(2 * time.Minute)
	e.wantRun("paused", pauseBuilderSilent)
	if got := e.reminders(); len(got) != 1 {
		t.Fatalf("reminded %d times, want once", len(got))
	}
}

// A builder still working, or one that answers the reminder by working on, is
// never paused by the reminder's window.
func TestWorkingBuilderIsNotReminded(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsRunning)
	e.watch(2 * SilenceWindow)
	if got := e.reminders(); len(got) != 0 {
		t.Fatalf("a working builder was reminded: %+v", got)
	}
	e.wantRun("running", "")
}

// A pane idle for less than the window, as between two tool calls, is not
// reminded: the clock restarts once the pane works again.
func TestBriefIdleIsNotReminded(t *testing.T) {
	e := newCEnv(t)
	eff := e.startedBuilder(runner.ObsCompleted)
	for i := 0; i < 5; i++ {
		e.watch(RemindWindow - time.Minute)
		e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, StateSeq: 6 + 2*i})
		e.watch(time.Minute)
		e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, StateSeq: 7 + 2*i})
	}
	if got := e.reminders(); len(got) != 0 {
		t.Fatalf("a builder between tool calls was reminded: %+v", got)
	}
	e.wantRun("running", "")
}

// A handback that is queued but cannot apply yet is not a missing handback, so
// the builder is not asked for another. The silence window still applies.
func TestQueuedHandbackIsNotReminded(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	run, cfg, err := e.c.load(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(RunDir(e.relay, run.ID), "q"), "waiting.json", "{}")
	for left := SilenceWindow + time.Minute; left > 0; left -= time.Minute {
		e.clk.advance(time.Minute)
		if err := e.c.watchSilence(ctx, run.ID, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.reminders(); len(got) != 0 {
		t.Fatalf("a queued handback was reminded: %+v", got)
	}
	e.wantRun("paused", pauseBuilderSilent)
}

// A reminder that could not be typed is not typed again: the tick that tried
// reports it, and the planner hears of the silence a remind window later.
func TestAReminderIsTriedOnce(t *testing.T) {
	e := newCEnv(t)
	e.startedBuilder(runner.ObsCompleted)
	e.tr.sendErr = func(d runner.Delivery) error {
		if d.Kind == effectBuilderReminder {
			return runner.NotSent(errors.New("pane busy"))
		}
		return nil
	}
	failed := 0
	for left := 2*RemindWindow + time.Minute; left > 0; left -= time.Minute {
		e.clk.advance(time.Minute)
		if err := e.c.Tick(ctx, e.runID); err != nil {
			if !runner.IsNotSent(err) {
				t.Fatalf("tick: %v", err)
			}
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d ticks reported the reminder, want 1", failed)
	}
	if e.tr.attempts != 1+1 { // the task prompt and one reminder
		t.Fatalf("%d sends, want the task and one reminder", e.tr.attempts)
	}
	e.wantRun("paused", pauseBuilderSilent)
}

// A repair attempt is a new attempt, so its builder can be reminded too.
func TestRepairAttemptIsRemindedAgain(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.requestChanges(task.ID, 1, workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "fix it"})
	e.tick()
	att := e.current(task.ID)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.tr.set(e.builderEffect(att).ID, runner.Observation{State: runner.ObsCompleted})
	e.tick()
	e.watch(RemindWindow + time.Minute)
	got := e.reminders()
	if len(got) != 1 || got[0].AttemptID != att.ID {
		t.Fatalf("the repair attempt was not reminded: %+v", got)
	}
}
