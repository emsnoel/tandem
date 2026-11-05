package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
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

// A run with only supported kinds keeps the rule whatever is open.
func TestStopLeavesEveryDeliveryOfASupportedRun(t *testing.T) {
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
}
