package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// stoppingEnv starts a run, gets its first task to a working builder and asks
// for a stop, as an operator does before closing the builder's pane.
func stoppingEnv(t *testing.T) (*cenv, workflow.Effect) {
	t.Helper()
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.tick()
	att := e.current(task.ID)
	eff := e.builderEffect(att)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	return e, eff
}

func cancelUndeliverable(d runner.Delivery) error {
	if d.Kind == "cancel" {
		return runner.NotSent(errors.New("agent target builder not found"))
	}
	return nil
}

// If the builder's pane closes during a stop, the cancel cannot be delivered.
// The run must stay stopping until a builder is back and observed stopped, so
// a later stop and archive still work.
func TestStopStaysStoppingWhenTheCancelCannotBeDelivered(t *testing.T) {
	e, eff := stoppingEnv(t)
	e.tr.sendErr = cancelUndeliverable
	for i := 0; i < 6; i++ {
		e.tick()
		e.clk.advance(10 * time.Second)
	}
	if state, code := e.runState(); state != "stopping" {
		t.Fatalf("an undeliverable cancel moved the stop to %s/%s", state, code)
	}
	// A builder comes back and has stopped: the stop completes and archives.
	e.tr.sendErr = nil
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsStopped, Nonce: eff.Nonce})
	for i := 0; i < 3; i++ {
		e.tick()
		e.clk.advance(10 * time.Second)
	}
	if state, code := e.runState(); state != "paused" || code != "stopped" {
		t.Fatalf("after the builder stopped: %s/%s", state, code)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		t.Fatalf("archive: %v", err)
	}
}

// A run an older tandem left as paused/dispatch_failed after a stop must be
// stoppable again, so the documented stop, recover, archive sequence works.
func TestStopAgainLeavesADispatchFailedPauseThatFollowedAStop(t *testing.T) {
	e, eff := stoppingEnv(t)
	if _, err := e.raw().Exec(`UPDATE runs SET state='paused', pause_code='dispatch_failed' WHERE id=?`, e.runID); err != nil {
		t.Fatal(err)
	}
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsStopped, Nonce: eff.Nonce})
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatalf("a second stop: %v", err)
	}
	if state, _ := e.runState(); state != "stopping" {
		t.Fatalf("a second stop left the run %s", state)
	}
	for i := 0; i < 3; i++ {
		e.tick()
		e.clk.advance(10 * time.Second)
	}
	if state, code := e.runState(); state != "paused" || code != "stopped" {
		t.Fatalf("after the second stop: %s/%s", state, code)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		t.Fatalf("archive: %v", err)
	}
}
