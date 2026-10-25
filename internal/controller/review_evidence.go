package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// reviewerFinished checks that a review prompt reached the attempt's reviewer
// and that its pane is now seen to be settled. A reviewer still working, one
// that cannot be observed, and one that was never asked are all "not finished".
func (c *Controller) reviewerFinished(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (runner.Observation, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return runner.Observation{}, err
	}
	var prompt *workflow.Effect
	for i := range effects {
		if e := effects[i]; e.Kind == effectReviewPrompt && e.AttemptID == att.ID {
			switch e.State {
			case "delivering", "acknowledged", "delivery_unknown":
				// Sent, or possibly sent. A result that names this job and its
				// patch could only have come from a reviewer that read the prompt.
				prompt = &effects[i]
			}
		}
	}
	if prompt == nil {
		return runner.Observation{}, fmt.Errorf("%w: no review prompt was delivered for attempt %s, so there is no reviewer whose result this could be", workflow.ErrIncompleteEvidence, att.ID)
	}
	obs, err := c.observeEffect(ctx, cfg, *prompt)
	if err != nil {
		return obs, fmt.Errorf("%w: the reviewer cannot be observed: %v", workflow.ErrIncompleteEvidence, err)
	}
	if obs.State != runner.ObsCompleted {
		return obs, fmt.Errorf("%w: the reviewer has not finished (observed %q); a result is taken only once its pane is settled", workflow.ErrIncompleteEvidence, obs.State)
	}
	return obs, nil
}

// treeStillReviewed checks the checkout still holds the tree that was reviewed.
// If not, the run pauses for a person and the error says what changed.
func (c *Controller) treeStillReviewed(ctx context.Context, run state.Run, cfg runConfig, reviewed string) error {
	now, err := c.captureStart(ctx, cfg)
	if err != nil {
		return err
	}
	if now == reviewed {
		return nil
	}
	return errors.Join(fmt.Errorf("%w: %w (reviewed %.12s, now %.12s), so the result says nothing about it; a new check and review are needed",
		workflow.ErrIncompleteEvidence, errTreeChanged, reviewed, now), c.d.State.SetRunState(ctx, run.ID, "paused", pauseSourceMutated))
}
