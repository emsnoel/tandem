package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// AbandonDelivery ends an open delivery on a person's statement that they are
// giving up on the prompt (state.AbandonDelivery), after looking at the pane it
// was typed into. A pane that is working or at a dialog may have the prompt,
// and one Herdr cannot answer for, or that reports no status, may too, so those
// refuse. A gone pane stays allowed because the route needs it: a discontinued
// pane can never come back, and tandem-pair refuses that kind. The planner's pane
// is not looked at: the planner runs this command,
// so its pane is working whether or not it read the prompt.
func (c *Controller) AbandonDelivery(ctx context.Context, runID, effectID, because, actor string) error {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	// An effect that cannot be read, or is another run's, is the store's refusal.
	if eff, err := c.d.State.GetEffect(ctx, effectID); err == nil && eff.RunID == run.ID {
		// A queued message with this nonce acknowledges the prompt.
		if _, queued := c.queuedAcknowledgements(ctx, run, cfg)[eff.ID]; queued {
			return fmt.Errorf("%w: the agent queued a message that acknowledges this prompt. Run tandem state recover --run %s, which acknowledges it and closes the delivery", workflow.ErrBusy, runID)
		}
		if !forPlanner(eff) {
			_, pane := cfg.agentFor(eff)
			if err := c.paneNotBusy(ctx, pane); err != nil {
				return err
			}
		}
	}
	statement, err := c.putJSON(ctx, map[string]string{"effect_id": effectID, "actor": actor, "reason": because})
	if err != nil {
		return err
	}
	return c.d.State.AbandonDelivery(ctx, runID, effectID, statement)
}

func (c *Controller) paneNotBusy(ctx context.Context, pane string) error {
	if pane == "" {
		return nil
	}
	ao, ok := c.d.Transport.(runner.AgentObserver)
	if !ok {
		return fmt.Errorf("%w: the pane %s cannot be observed here", workflow.ErrBusy, pane)
	}
	obs, err := ao.ObserveAgent(ctx, pane)
	switch {
	case errors.Is(err, runner.ErrAgentGone):
		return nil
	case err != nil:
		return fmt.Errorf("%w: the pane %s could not be read, so it may have the prompt: %v", workflow.ErrBusy, pane, err)
	case obs.State == runner.ObsRunning:
		return fmt.Errorf("%w: the pane %s is working or at a dialog, so it may have the prompt. Let it finish or clear the dialog, then try again", workflow.ErrBusy, pane)
	case obs.State == runner.ObsUnknown:
		return fmt.Errorf("%w: the pane %s reports no status, so it may have the prompt. Look at it and try again, or close it if it is stuck", workflow.ErrBusy, pane)
	}
	return nil
}
