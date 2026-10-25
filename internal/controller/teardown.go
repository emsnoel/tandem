package controller

import (
	"context"

	"github.com/emsnoel/tandem/internal/workflow"
)

// TaskEvidence is what one task of a run lacks before its work can be signed
// off. Unmet is empty when the task's accepted attempt still satisfies
// acceptance from its persisted evidence; otherwise it names "acceptance" (no
// attempt was accepted) or what workflow.Unmet reports for the accepted one.
type TaskEvidence struct {
	ID      string   `json:"id"`
	Ordinal int      `json:"ordinal"`
	Unmet   []string `json:"unmet"`
}

// RunEvidence is a run's own record of its work, for a teardown to decide on.
type RunEvidence struct {
	RunID string         `json:"run_id"`
	State string         `json:"state"`
	Tasks []TaskEvidence `json:"tasks"`
}

// TeardownEvidence reads, for every task of the run, whether an attempt was
// accepted and whether that attempt's check and review (or their waivers) still
// hold for its final tree. It applies the same rule acceptance does, so a task
// carried over from a legacy build without evidence is reported as lacking its
// check and review. It writes nothing.
func (c *Controller) TeardownEvidence(ctx context.Context, runID string) (RunEvidence, error) {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return RunEvidence{}, err
	}
	tasks, err := c.d.State.RunTasks(ctx, runID)
	if err != nil {
		return RunEvidence{}, err
	}
	ev := RunEvidence{RunID: run.ID, State: run.State}
	obs := workflow.Observed{NoCheckPolicy: cfg.CheckCommand == ""}
	for _, t := range tasks {
		te := TaskEvidence{ID: t.ID, Ordinal: t.Ordinal}
		if t.AcceptedAttemptID == "" {
			te.Unmet = []string{"acceptance"}
		} else {
			att, err := c.d.State.GetAttempt(ctx, t.AcceptedAttemptID)
			if err != nil {
				return RunEvidence{}, err
			}
			f, err := c.d.State.AcceptanceFacts(ctx, att, obs)
			if err != nil {
				return RunEvidence{}, err
			}
			te.Unmet = workflow.Unmet(att, f)
		}
		ev.Tasks = append(ev.Tasks, te)
	}
	return ev, nil
}
