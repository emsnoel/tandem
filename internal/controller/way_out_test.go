package controller

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// seedPause leaves a run paused with this code, its first task prompt typed. An
// open prompt was never acknowledged. Otherwise the builder took it. The builder
// is idle either way, as a stopped run needs.
func (e *cenv) seedPause(code string, open bool) workflow.Effect {
	e.t.Helper()
	e.start()
	e.tick()
	task := e.tasks()[0]
	eff := e.builderEffect(e.current(task.ID))
	if !open {
		e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	}
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted})
	switch code {
	case pauseStopped:
		e.stopAndSettle()
		return eff
	case pauseWriterUnknown:
		if err := e.st.MarkDeliveryUnknown(ctx, eff.ID, eff.Fence, ""); err != nil && open {
			e.t.Fatal(err)
		}
	case pauseNoReviewer:
		e.pinNoReviewer()
	case pauseAgentUnsupported:
		e.pinKinds("opencode", "claude")
	}
	if err := e.st.SetRunState(ctx, e.runID, "paused", code); err != nil {
		e.t.Fatal(err)
	}
	return eff
}

// pinNoReviewer rewrites the run's stored configuration without a reviewer, as a
// run started before every run needed one has.
func (e *cenv) pinNoReviewer() {
	e.t.Helper()
	run, err := e.st.GetRun(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg, err := e.c.config(ctx, run)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg.ReviewerKind, cfg.ReviewerSession = "", ""
	hash, err := e.c.putJSON(ctx, cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.raw().Exec(`UPDATE runs SET config_hash=? WHERE id=?`, hash, e.runID); err != nil {
		e.t.Fatal(err)
	}
}

// stopToPaused is what `tandem build stop` and then `tandem build wait --until
// paused --tick` do.
func (e *cenv) stopToPaused() {
	e.t.Helper()
	if err := e.c.Stop(ctx, e.runID); err != nil {
		e.t.Fatalf("stop: %v", err)
	}
	for i := 0; i < 4; i++ {
		e.tick()
		if st, _ := e.runState(); st == "paused" {
			break
		}
		e.clk.advance(10 * time.Second)
	}
	if st, code := e.runState(); st != "paused" || code != pauseStopped {
		e.t.Fatalf("after the stop the run is %s/%s", st, code)
	}
}

// settleNotRead abandons a prompt. A pane the test said nothing about is idle,
// because a pane in an unknown status refuses.
func (e *cenv) settleNotRead(effectID string) error {
	for _, pane := range []string{"builder-session", "reviewer-session"} {
		if _, ok := e.tr.agents[pane]; !ok {
			e.tr.setAgent(pane, runner.Observation{State: runner.ObsCompleted})
		}
	}
	return e.c.AbandonDelivery(ctx, e.runID, effectID, "the pane never showed it", "owner")
}

// archiveRefused checks the refusal names the command for this effect.
func (e *cenv) archiveRefused(effectID string) {
	e.t.Helper()
	err := e.st.ArchiveRun(ctx, e.runID)
	want := "tandem state settle-delivery --run " + e.runID + " --effect " + effectID + " --abandon"
	if !errors.Is(err, workflow.ErrBusy) || !strings.Contains(err.Error(), want) {
		e.t.Fatalf("archive: %v", err)
	}
}

// startsAgain is the last step of every restart: a new run in the same checkout
// types its first prompt.
func (e *cenv) startsAgain() {
	e.t.Helper()
	e.start()
	sends := len(e.tr.sends)
	e.tick()
	if st, code := e.runState(); st != "running" {
		e.t.Fatalf("the new run is %s/%s", st, code)
	}
	if len(e.tr.sends) != sends+1 || e.tr.sends[sends].Kind != workflow.EffectBuilderDispatch {
		e.t.Fatalf("the new run sent %+v", e.tr.sends[sends:])
	}
}

// Every pause code leaves through the restart route the notes print: stop, wait,
// recover, archive, start again. With a prompt nobody confirmed, the archive's
// refusal names settle-delivery, which is the only way past it.
func TestEveryPauseHasAWayOut(t *testing.T) {
	for _, code := range PauseCodes() {
		if code == workflow.PauseStopping {
			continue // a run mid-stop, covered by the gone-pane tests below
		}
		for _, open := range []bool{false, true} {
			if code == pauseWriterUnknown && !open {
				continue // this pause means the prompt is open
			}
			t.Run(fmt.Sprintf("%s, open prompt %v", code, open), func(t *testing.T) {
				e := newCEnv(t)
				eff := e.seedPause(code, open)
				e.stopToPaused()
				if err := e.c.Recover(ctx, e.runID); err != nil {
					t.Fatalf("recover: %v", err)
				}
				got, err := e.st.GetEffect(ctx, eff.ID)
				if err != nil {
					t.Fatal(err)
				}
				// A discontinued builder's prompt is ended by the stop itself.
				if open && code != pauseAgentUnsupported {
					if got.State != "delivering" && got.State != "delivery_unknown" {
						t.Fatalf("the prompt is %s", got.State)
					}
					e.archiveRefused(eff.ID)
					if err := e.settleNotRead(eff.ID); err != nil {
						t.Fatalf("settle: %v", err)
					}
				}
				if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
					t.Fatalf("archive: %v", err)
				}
				e.startsAgain()
			})
		}
	}
}

// writer_unknown holds its lease, so nothing can be settled until a stop has
// seen the builder quiet and released the writer. The note prints that order.
func TestWriterUnknownSettlesOnlyAfterTheStop(t *testing.T) {
	e := newCEnv(t)
	eff := e.seedPause(pauseWriterUnknown, true)
	if err := e.settleNotRead(eff.ID); !errors.Is(err, workflow.ErrBusy) || !strings.Contains(err.Error(), "tandem build stop") {
		t.Fatalf("settling under a held lease: %v", err)
	}
	if got, err := e.st.GetEffect(ctx, eff.ID); err != nil || got.State != "delivery_unknown" {
		t.Fatalf("the prompt is %s (%v)", got.State, err)
	}
	e.stopToPaused()
	if err := e.settleNotRead(eff.ID); err != nil {
		t.Fatal(err)
	}
}

// The note's other branch: the builder did read the prompt, and its
// acknowledgement waits in the mailbox. One state recover has to be enough,
// because the note says so.
func TestWriterUnknownClearsOnOneRecoverOnceTheBuilderAcknowledged(t *testing.T) {
	e := newCEnv(t)
	e.seedPause(pauseWriterUnknown, true)
	e.queueClaimed(e.tasks()[0].ID, workflow.EventBuilderStarted, "{}\n")
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if st, code := e.runState(); st != "running" {
		t.Fatalf("after one recover the run is %s/%s", st, code)
	}
}

// A run stopped before the upgrade: its stop finished, so a second stop replays
// the old event and tickStopping never runs to cancel the prompt to the now
// discontinued pane. settle-delivery is what ends it.
func TestStopFinishedBeforeTheUpgradeLeavesThroughSettleDelivery(t *testing.T) {
	e := newCEnv(t)
	_, prompt := e.openReviewPrompt()
	e.stopAndSettle()
	e.pinKinds("codex", "opencode")
	version := func() int64 {
		run, err := e.st.GetRun(ctx, e.runID)
		if err != nil {
			t.Fatal(err)
		}
		return run.Version
	}
	before := version()
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if st, code := e.runState(); st != "paused" || code != pauseStopped || version() != before {
		t.Fatalf("the second stop moved the run to %s/%s", st, code)
	}
	if got, err := e.st.GetEffect(ctx, prompt.ID); err != nil || got.State != "delivering" {
		t.Fatalf("the prompt is %s (%v)", got.State, err)
	}
	e.archiveRefused(prompt.ID)
	if err := e.settleNotRead(prompt.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	e.startsAgain()
}

// A stop with the builder's pane gone stays stopping, and it holds the writer,
// so settle-delivery cannot end it. The way out is to bring the agent back. Then
// an open prompt leaves through settle-delivery like any other.
func TestGoneBuilderPaneLeavesAStopThroughTandemPair(t *testing.T) {
	for _, open := range []bool{false, true} {
		t.Run(fmt.Sprintf("open prompt %v", open), func(t *testing.T) {
			e := newCEnv(t)
			eff := e.seedPause(pauseAgentExited, open)
			e.tr.sendErr = cancelUndeliverable
			e.tr.obsErr = fmt.Errorf("%w: builder-session", runner.ErrAgentGone)
			if err := e.c.Stop(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				if err := e.c.Tick(ctx, e.runID); !errors.Is(err, runner.ErrAgentGone) {
					t.Fatalf("tick: %v", err)
				}
				e.clk.advance(10 * time.Second)
			}
			if st, _ := e.runState(); st != "stopping" {
				t.Fatalf("the run is %s", st)
			}
			if open {
				if err := e.settleNotRead(eff.ID); !errors.Is(err, workflow.ErrBusy) {
					t.Fatalf("settling while the stop holds the writer: %v", err)
				}
			}

			// tandem-pair brings the agent back under its name, and it is idle.
			e.tr.sendErr, e.tr.obsErr = nil, nil
			for i := 0; i < 4; i++ {
				e.tick()
				e.clk.advance(10 * time.Second)
			}
			if st, code := e.runState(); st != "paused" || code != pauseStopped {
				t.Fatalf("with the agent back the run is %s/%s", st, code)
			}
			if err := e.c.Recover(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			if open {
				e.archiveRefused(eff.ID)
				if err := e.settleNotRead(eff.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
				t.Fatalf("archive: %v", err)
			}
			e.startsAgain()
		})
	}
}
