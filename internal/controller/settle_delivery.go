package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// SettleDelivery ends an open delivery on a person's statement that the agent
// never read the prompt (state.SettleDeliveryNotRead), after looking at the pane
// it was typed into. A pane that is working or at a dialog may have the prompt,
// and one Herdr cannot answer for may too, so those refuse. A gone pane is reading
// nothing. The planner's pane is not looked at: the planner runs this command,
// so its pane is working whether or not it read the prompt.
func (c *Controller) SettleDelivery(ctx context.Context, runID, effectID, because, actor string) error {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	// An effect that cannot be read, or is another run's, is the store's refusal.
	if eff, err := c.d.State.GetEffect(ctx, effectID); err == nil && eff.RunID == run.ID && !forPlanner(eff) {
		_, pane := cfg.agentFor(eff)
		if err := c.paneNotBusy(ctx, pane); err != nil {
			return err
		}
	}
	reason, err := c.put(ctx, because)
	if err != nil {
		return err
	}
	return c.d.State.SettleDeliveryNotRead(ctx, runID, effectID, reason, actor)
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
		return fmt.Errorf("%w: the pane %s is working or at a dialog, so it may have the prompt. Let it finish or clear the dialog, then settle again", workflow.ErrBusy, pane)
	}
	return nil
}
