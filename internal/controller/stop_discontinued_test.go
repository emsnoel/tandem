package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// pinKinds rewrites the run's stored kinds, which is how a run made before the
// upgrade looks to this tandem. A tick refuses to deliver for a kind with no
// adapter, so the prompts are delivered first and the kinds changed after.
func (e *cenv) pinKinds(builder, reviewer string) {
	e.t.Helper()
	run, err := e.st.GetRun(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	raw, err := e.art.ReadAll(ctx, run.ConfigHash, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		e.t.Fatal(err)
	}
	cfg["builder_kind"], cfg["reviewer_kind"] = builder, reviewer
	hash, err := e.c.putJSON(ctx, cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(e.stRoot, "state.sqlite"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE runs SET config_hash=? WHERE id=?`, hash, e.runID); err != nil {
		e.t.Fatal(err)
	}
}

// openReviewPrompt reaches a typed, unanswered review prompt on a real run: the
// builder acknowledged its task and handed back, and the reviewer's job is open.
func (e *cenv) openReviewPrompt() (workflow.Attempt, workflow.Effect) {
	e.t.Helper()
	e.start()
	task := e.tasks()[0]
	e.tick()
	att := e.current(task.ID)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	write(e.t, e.root, "work.txt", "work\n")
	e.settle(att)
	e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	for i := 0; i < 4 && len(e.delivered()) < 2; i++ {
		e.tick()
	}
	prompts := e.effects(effectReviewPrompt)
	if len(prompts) != 1 || prompts[0].State != "delivering" {
		e.t.Fatalf("review prompts: %+v", prompts)
	}
	return att, prompts[0]
}

func (e *cenv) stopAndSettle() {
	e.t.Helper()
	if err := e.c.Stop(ctx, e.runID); err != nil {
		e.t.Fatal(err)
	}
	e.tick()
	if st, code := e.runState(); st != "paused" || code != pauseStopped {
		e.t.Fatalf("after the stop the run is %s/%s", st, code)
	}
}

func (e *cenv) reviewerJobStates(att workflow.Attempt) []string {
	e.t.Helper()
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, j := range jobs {
		out = append(out, j.State)
	}
	return out
}

// A discontinued reviewer can never answer, so neither its open prompt nor the
// job waiting on it holds the archive.
func TestStopCancelsADiscontinuedReviewersOpenPrompt(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		e := newCEnv(t)
		att, prompt := e.openReviewPrompt()
		if unknown {
			if err := e.st.MarkDeliveryUnknown(ctx, prompt.ID, prompt.Fence, ""); err != nil {
				t.Fatal(err)
			}
		}
		e.pinKinds("codex", "opencode")
		sends := len(e.tr.sends)
		e.stopAndSettle()
		if got, err := e.st.GetEffect(ctx, prompt.ID); err != nil || got.State != "cancelled" {
			t.Fatalf("unknown=%v: the prompt is %s (%v)", unknown, got.State, err)
		}
		if got := e.reviewerJobStates(att); len(got) != 1 || got[0] != "cancelled" {
			t.Fatalf("unknown=%v: reviewer jobs %v", unknown, got)
		}
		if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
			t.Fatalf("unknown=%v: %v", unknown, err)
		}
		for _, d := range e.tr.sends[sends:] {
			if d.Kind != "cancel" {
				t.Fatalf("unknown=%v: typed a %s after the upgrade", unknown, d.Kind)
			}
		}
	}
}

// Scoped by who the delivery was for: a supported reviewer can still answer,
// even though the builder's kind is discontinued.
func TestStopLeavesASupportedReviewersOpenPrompt(t *testing.T) {
	e := newCEnv(t)
	_, prompt := e.openReviewPrompt()
	e.pinKinds("opencode", "claude")
	e.stopAndSettle()
	if got, err := e.st.GetEffect(ctx, prompt.ID); err != nil || got.State != "delivering" {
		t.Fatalf("the prompt is %s (%v)", got.State, err)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("archive: %v", err)
	}
}

// plannerEffects are the deliveries addressed to the planner. The planner's kind
// is stored as the literal "planner", which is no adapter's kind, and that must
// not make its deliveries look discontinued.
var plannerEffects = []string{effectNotify, effectVerifyPrompt, effectRecoveryDecision}

// openPlannerDelivery leaves a typed, unconfirmed planner delivery of this kind
// as the only delivery open: the builder has acknowledged its own prompt.
func (e *cenv) openPlannerDelivery(kind string, unknown bool) workflow.Effect {
	e.t.Helper()
	e.start()
	task := e.tasks()[0]
	e.tick()
	att := e.current(task.ID)
	dispatch := e.builderEffect(att)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.settle(att)
	prompt, err := e.c.put(ctx, "for the planner\n")
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.InsertDerivedEffect(ctx, dispatch.ID, kind, prompt); err != nil {
		e.t.Fatal(err)
	}
	eff, err := e.st.ClaimEffectKinds(ctx, "tester", e.runID, []string{kind})
	if err != nil {
		e.t.Fatal(err)
	}
	if unknown {
		if err := e.st.MarkDeliveryUnknown(ctx, eff.ID, eff.Fence, ""); err != nil {
			e.t.Fatal(err)
		}
	}
	return eff
}

// A delivery to the planner keeps the nonce rule on every run: the planner's
// pane is never discontinued.
func TestStopLeavesEveryDeliveryOfASupportedRun(t *testing.T) {
	t.Run("a builder dispatch", func(t *testing.T) {
		e := newCEnv(t)
		e.start()
		e.tick()
		eff := e.builderEffect(e.current(e.tasks()[0].ID))
		e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted})
		e.stopAndSettle()
		if got, err := e.st.GetEffect(ctx, eff.ID); err != nil || got.State != "delivering" {
			t.Fatalf("the prompt is %s (%v)", got.State, err)
		}
		if err := e.st.ArchiveRun(ctx, e.runID); !errors.Is(err, workflow.ErrBusy) {
			t.Fatalf("archive: %v", err)
		}
	})
	for _, kinds := range [][2]string{{"", ""}, {"codex", "opencode"}, {"opencode", "claude"}, {"opencode", "opencode"}} {
		for _, kind := range plannerEffects {
			for _, unknown := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s, unknown %v, builder %q reviewer %q", kind, unknown, kinds[0], kinds[1]), func(t *testing.T) {
					e := newCEnv(t)
					eff := e.openPlannerDelivery(kind, unknown)
					want := "delivering"
					if unknown {
						want = "delivery_unknown"
					}
					if kinds[0] != "" {
						e.pinKinds(kinds[0], kinds[1])
					}
					e.stopAndSettle()
					if got, err := e.st.GetEffect(ctx, eff.ID); err != nil || got.State != want {
						t.Fatalf("the delivery is %s, want %s (%v)", got.State, want, err)
					}
					err := e.st.ArchiveRun(ctx, e.runID)
					if !errors.Is(err, workflow.ErrBusy) || !strings.Contains(err.Error(), "has 1 delivery whose outcome is open") {
						t.Fatalf("archive: %v", err)
					}
				})
			}
		}
	}
}

// A discontinued run waits for a person. An idle pane must not let a tick spend
// a repair allowance on it, because tandem could never type the repair.
func TestDiscontinuedRunSpendsNoRepairAllowance(t *testing.T) {
	for _, kinds := range [][2]string{{"opencode", "claude"}, {"codex", "opencode"}} {
		t.Run(kinds[0]+"/"+kinds[1], func(t *testing.T) {
			e := newCEnv(t)
			// The check failed while the builder's pane was still busy, so an older
			// tandem left the repair for a later tick and the run paused.
			e.startRecovery(true)
			e.chk.exit = 1
			e.tick()
			att := e.current("task-1")
			eff := e.builderEffect(att)
			e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
			write(t, e.root, "work1.txt", "broken\n")
			e.settle(att)
			e.mustSend("task-1", workflow.EventBuilderSubmitted, "{}\n")
			e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
			for i := 0; i < 4; i++ {
				e.tick()
			}
			if n := e.usedRepairs(); n != 0 {
				t.Fatalf("setup: %d repairs spent while the pane was busy", n)
			}
			if err := e.st.SetRunState(ctx, e.runID, "paused", workflow.PauseCheckFailed); err != nil {
				t.Fatal(err)
			}
			e.pinKinds(kinds[0], kinds[1])
			// The pane is idle now, as it is after the upgrade.
			e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, Nonce: eff.Nonce})
			before := e.attemptsOf("task-1")
			for i := 0; i < 3; i++ {
				e.tick()
			}
			if st, code := e.runState(); st != "paused" || code != workflow.PauseCheckFailed {
				t.Fatalf("run is %s/%s", st, code)
			}
			if n := e.usedRepairs(); n != 0 {
				t.Fatalf("%d repairs spent", n)
			}
			if n := e.attemptsOf("task-1"); n != before {
				t.Fatalf("%d attempts, was %d", n, before)
			}
		})
	}
}
