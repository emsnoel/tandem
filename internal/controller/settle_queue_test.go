package controller

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// queuedOnStopped stops a run with its prompt open, then queues what the builder
// sent late, as tandem-handback does once its turn starts.
func queuedOnStopped(t *testing.T, kind string, taken bool) (*cenv, workflow.Effect) {
	t.Helper()
	e := newCEnv(t)
	eff := e.seedPause(pauseDispatch, true)
	e.stopToPaused()
	claimed := e.queueClaimed(e.tasks()[0].ID, kind, "{}\n")
	if !taken {
		if err := os.Rename(claimed, strings.TrimSuffix(claimed, ".taken")); err != nil {
			t.Fatal(err)
		}
	}
	return e, eff
}

// A late acknowledgement proves the prompt was read. Abandoning it then would
// record something the mailbox disproves, so the refusal says to read the
// mailbox. state recover does, the nonce closes the delivery, and the run stays
// stopped.
func TestAQueuedMessageClosesTheDeliveryOnAStoppedRun(t *testing.T) {
	for name, tc := range map[string]struct {
		kind  string
		taken bool
	}{
		"a queued acknowledgement":  {workflow.EventBuilderStarted, false},
		"a claimed acknowledgement": {workflow.EventBuilderStarted, true},
		"a queued submission":       {workflow.EventBuilderSubmitted, false},
		"a queued stuck report":     {workflow.EventBuilderStuck, false},
	} {
		t.Run(name, func(t *testing.T) {
			e, eff := queuedOnStopped(t, tc.kind, tc.taken)
			err := e.settleNotRead(eff.ID)
			if !errors.Is(err, workflow.ErrBusy) || !strings.Contains(err.Error(), "tandem state recover --run "+e.runID) {
				t.Fatalf("abandoning with a message queued: %v", err)
			}
			if got, err := e.st.GetEffect(ctx, eff.ID); err != nil || got.State != "delivering" {
				t.Fatalf("the refusal moved the prompt to %s (%v)", got.State, err)
			}
			sends := len(e.tr.sends)
			if err := e.c.Recover(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			if got, err := e.st.GetEffect(ctx, eff.ID); err != nil || got.State != "acknowledged" {
				t.Fatalf("after recover the prompt is %s (%v)", got.State, err)
			}
			if st, code := e.runState(); st != "paused" || code != pauseStopped {
				t.Fatalf("recover moved the run to %s/%s", st, code)
			}
			if len(e.tr.sends) != sends {
				t.Fatalf("typed %+v", e.tr.sends[sends:])
			}
			if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
				t.Fatalf("archive: %v", err)
			}
			e.startsAgain()
		})
	}
}

// A message that does not carry the prompt's nonce proves nothing, so it must
// not hold the way out shut.
func TestAQueuedMessageWithTheWrongNonceDoesNotBlockAbandoning(t *testing.T) {
	e, eff := queuedOnStopped(t, workflow.EventBuilderStarted, false)
	path := filepath.Join(RunDir(e.relay, e.runID), "q")
	files, err := filepath.Glob(filepath.Join(path, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.Nonce = strings.Repeat("f", 32)
	raw, _ = json.Marshal(env)
	if err := os.WriteFile(files[0], raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.settleNotRead(eff.ID); err != nil {
		t.Fatal(err)
	}
}

// admittedOpenPrompt reaches a typed, unconfirmed prompt under a budget, stopped.
func admittedOpenPrompt(t *testing.T, noSession bool) (*cenv, workflow.Effect) {
	t.Helper()
	e := newCEnv(t)
	e.tr.noSession = noSession
	e.startEnforced(1000)
	e.tick()
	eff := e.builderEffect(e.current(e.tasks()[0].ID))
	if got := e.codexBudget().InputReserved; got != 60 {
		t.Fatalf("reserved %d", got)
	}
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted})
	e.stopToPaused()
	return e, eff
}

func (e *cenv) sourceOf(eff workflow.Effect) state.Source {
	e.t.Helper()
	src, err := e.st.Source(ctx, sourceID(eff.ID))
	if err != nil {
		e.t.Fatal(err)
	}
	return src
}

// With no session bound nothing can have been spent under this launch: the
// reservation goes back, the source ends, and the collaboration can close.
func TestAbandoningWithNoSessionReleasesTheReservationAndEndsTheSource(t *testing.T) {
	e, eff := admittedOpenPrompt(t, true)
	if err := e.settleNotRead(eff.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.codexBudget().InputReserved; got != 0 {
		t.Fatalf("still reserved: %d", got)
	}
	if got := e.sourceOf(eff).Status; got != "abandoned" {
		t.Fatalf("the source is %s", got)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.CloseCollaboration(ctx, enforcedCollab); err != nil {
		t.Fatal(err)
	}
	status, err := e.st.CollaborationStatus(ctx, enforcedCollab)
	if err != nil || status != "closed" {
		t.Fatalf("the collaboration is %s (%v)", status, err)
	}
}

// With a session bound its usage may be real, so nothing is given back.
func TestAbandoningWithABoundSessionLeavesTheReservation(t *testing.T) {
	e, eff := admittedOpenPrompt(t, false)
	before := e.sourceOf(eff).Status
	if err := e.settleNotRead(eff.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.codexBudget().InputReserved; got != 60 {
		t.Fatalf("reserved %d", got)
	}
	if got := e.sourceOf(eff).Status; got != before {
		t.Fatalf("the source moved from %s to %s", before, got)
	}
}
