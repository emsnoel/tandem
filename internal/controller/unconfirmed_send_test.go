package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// unconfirmedSend starts a run whose builder prompt was typed, but whose turn
// the transport never saw start.
func (e *cenv) unconfirmedSend() {
	e.t.Helper()
	e.tr.sendErr = func(d runner.Delivery) error {
		if d.Kind == workflow.EffectBuilderDispatch {
			return errors.New("the prompt was typed but it never started working")
		}
		return nil
	}
	e.start()
	e.tick()
	if got := e.effects(workflow.EffectBuilderDispatch)[0].State; got != "delivery_unknown" {
		e.t.Fatalf("fixture: the effect is %s", got)
	}
}

// sendAttempts counts every try at typing a prompt, including one that failed.
func (e *cenv) sendAttempts() int {
	e.tr.mu.Lock()
	defer e.tr.mu.Unlock()
	return e.tr.attempts
}

func TestUnconfirmedBuilderSendDoesNotPauseWhileAnAcknowledgementCanArrive(t *testing.T) {
	e := newCEnv(t)
	e.unconfirmedSend()
	e.wantRun("running", "")

	e.clk.advance(state.BuilderAckGrace / 2)
	e.tick()
	e.wantRun("running", "")
}

func TestLateAcknowledgementContinuesTheRunWithoutAPause(t *testing.T) {
	e := newCEnv(t)
	e.unconfirmedSend()

	e.clk.advance(9 * time.Second)
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.tick()

	e.wantRun("running", "")
	if got := e.effects(workflow.EffectBuilderDispatch)[0].State; got != "acknowledged" {
		t.Fatalf("the effect is %s, want acknowledged", got)
	}
	if got := e.sendAttempts(); got != 1 {
		t.Fatalf("the prompt was sent %d times", got)
	}
}

func TestUnconfirmedBuilderSendPausesOnceNoAcknowledgementCame(t *testing.T) {
	e := newCEnv(t)
	e.unconfirmedSend()

	e.clk.advance(state.BuilderAckGrace + time.Second)
	e.tick()

	e.wantRun("paused", pauseWriterUnknown)
	if got := e.sendAttempts(); got != 1 {
		t.Fatalf("an uncertain prompt was sent again: %d", got)
	}
}
