package controller

import (
	"context"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/state"
)

// cancelDeliveriesToDiscontinued ends the open deliveries addressed to a pane
// whose kind has no adapter. That pane can never return the nonce that would
// settle them, and tandem can never type to it again, so nothing else can. It
// runs only once the writer is seen to be quiet. A delivery addressed to a
// supported kind's pane, in a mixed run too, keeps the nonce rule, and so does
// every delivery to the planner.
func (c *Controller) cancelDeliveriesToDiscontinued(ctx context.Context, run state.Run, cfg runConfig) error {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, e := range effects {
		if (e.State != "delivering" && e.State != "delivery_unknown") || forPlanner(e) {
			continue
		}
		kind, _ := cfg.agentFor(e)
		if agent.FirstUnsupported(kind) == "" {
			continue
		}
		// The job first: if the effect's write fails, the next tick finds it still
		// open and does both again.
		if e.Kind == effectReviewPrompt {
			if err := c.d.State.CancelReviewJobs(ctx, e.AttemptID); err != nil {
				return err
			}
		}
		if err := c.d.State.CancelOpenDelivery(ctx, e.ID); err != nil {
			return err
		}
	}
	return nil
}
