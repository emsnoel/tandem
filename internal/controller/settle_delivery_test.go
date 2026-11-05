package controller

import (
	"errors"
	"fmt"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Every kind of open delivery can be settled on a stopped run, and settling one
// types nothing: tandem never retypes an uncertain prompt.
func TestSettleDeliveryOnEveryOpenKind(t *testing.T) {
	check := func(t *testing.T, e *cenv, eff workflow.Effect) {
		t.Helper()
		e.stopToPaused()
		e.archiveRefused(eff.ID)
		sends := len(e.tr.sends)
		if err := e.settleNotRead(eff.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := e.st.GetEffect(ctx, eff.ID); err != nil || got.State != "cancelled" {
			t.Fatalf("the prompt is %s (%v)", got.State, err)
		}
		if err := e.c.Recover(ctx, e.runID); err != nil {
			t.Fatal(err)
		}
		e.tick()
		if len(e.tr.sends) != sends {
			t.Fatalf("typed %+v after the settle", e.tr.sends[sends:])
		}
		if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
			t.Fatalf("archive: %v", err)
		}
		e.startsAgain()
	}
	t.Run(workflow.EffectBuilderDispatch, func(t *testing.T) {
		e := newCEnv(t)
		check(t, e, e.seedPause(pauseDispatch, true))
	})
	t.Run(effectReviewPrompt, func(t *testing.T) {
		e := newCEnv(t)
		att, prompt := e.openReviewPrompt()
		e.stopToPaused()
		e.archiveRefused(prompt.ID)
		if err := e.settleNotRead(prompt.ID); err != nil {
			t.Fatal(err)
		}
		if got := e.reviewerJobStates(att); len(got) != 1 || got[0] != "cancelled" {
			t.Fatalf("reviewer jobs %v", got)
		}
		if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
			t.Fatalf("archive: %v", err)
		}
		e.startsAgain()
	})
	for _, kind := range plannerEffects {
		t.Run(kind, func(t *testing.T) {
			e := newCEnv(t)
			check(t, e, e.openPlannerDelivery(kind, true))
		})
	}
}

// The pane a prompt was typed into decides whether a person may say it was not
// read. A working or blocked one refuses, and so does one Herdr cannot answer
// for. A gone one cannot be reading anything. The planner's pane is not looked
// at, because the planner is who runs this command, so it is always working.
func TestSettleDeliveryLooksAtThePaneThePromptWentTo(t *testing.T) {
	gone := fmt.Errorf("%w: pane", runner.ErrAgentGone)
	for name, tc := range map[string]struct {
		pane    runner.Observation
		err     error
		refused bool
	}{
		"working":   {pane: runner.Observation{State: runner.ObsRunning}, refused: true},
		"no answer": {err: errors.New("herdr did not answer"), refused: true},
		"idle":      {pane: runner.Observation{State: runner.ObsCompleted}},
		"gone":      {err: gone},
		"not known": {pane: runner.Observation{State: runner.ObsUnknown}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			_, prompt := e.openReviewPrompt()
			e.stopToPaused()
			e.tr.setAgent("reviewer-session", tc.pane)
			if tc.err != nil {
				e.tr.setAgentErr("reviewer-session", tc.err)
			}
			err := e.settleNotRead(prompt.ID)
			if tc.refused != (err != nil) || (tc.refused && !errors.Is(err, workflow.ErrBusy)) {
				t.Fatalf("err = %v", err)
			}
			want := "cancelled"
			if tc.refused {
				want = "delivering"
			}
			if got, err := e.st.GetEffect(ctx, prompt.ID); err != nil || got.State != want {
				t.Fatalf("the prompt is %s (%v)", got.State, err)
			}
		})
	}
	t.Run("the planner's pane is working", func(t *testing.T) {
		e := newCEnv(t)
		eff := e.openPlannerDelivery(effectNotify, true)
		e.stopToPaused()
		e.tr.setAgent("planner-session", runner.Observation{State: runner.ObsRunning})
		if err := e.settleNotRead(eff.ID); err != nil {
			t.Fatal(err)
		}
	})
}

// The archive's refusal says state recover ends an open job, so it has to end
// every kind a stopped run can leave: a review whose prompt was taken, or failed
// to send, and a check that never finished.
func TestRecoverEndsEveryOpenJobOfAStoppedRun(t *testing.T) {
	for name, leave := range map[string]func(e *cenv, att workflow.Attempt, prompt workflow.Effect){
		"a review the reviewer took": func(e *cenv, _ workflow.Attempt, prompt workflow.Effect) {
			if err := e.st.AcknowledgeEffect(ctx, prompt.ID, prompt.Nonce); err != nil {
				e.t.Fatal(err)
			}
		},
		"a review prompt that failed to send": func(e *cenv, _ workflow.Attempt, prompt workflow.Effect) {
			if _, err := e.raw().Exec(`UPDATE effects SET state='failed' WHERE id=?`, prompt.ID); err != nil {
				e.t.Fatal(err)
			}
		},
		"a check that never finished": func(e *cenv, att workflow.Attempt, prompt workflow.Effect) {
			if err := e.st.AcknowledgeEffect(ctx, prompt.ID, prompt.Nonce); err != nil {
				e.t.Fatal(err)
			}
			if _, err := e.raw().Exec(`INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind)
				SELECT ?,attempt_id,'check','running',input_hash,config_hash,'check' FROM jobs WHERE attempt_id=? AND kind='reviewer'`, derive(att.ID, "lost-check"), att.ID); err != nil {
				e.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			att, prompt := e.openReviewPrompt()
			leave(e, att, prompt)
			e.stopToPaused()
			if err := e.c.Recover(ctx, e.runID); err != nil {
				t.Fatal(err)
			}
			if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
				t.Fatalf("archive: %v", err)
			}
			e.startsAgain()
		})
	}
}
