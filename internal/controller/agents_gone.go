package controller

import (
	"context"
	"errors"

	"github.com/emsnoel/tandem/internal/runner"
)

// goneAgent names the first of the run's builder and reviewer that the host
// says is gone, or "". Only that answer counts: a host that does not answer may
// come back, and acting on a guess would hold or release a run wrongly.
func (c *Controller) goneAgent(ctx context.Context, cfg runConfig) string {
	ao, ok := c.d.Transport.(runner.AgentObserver)
	if !ok {
		return ""
	}
	for _, name := range []string{cfg.BuilderSession, cfg.ReviewerSession} {
		if name == "" {
			continue
		}
		if _, err := ao.ObserveAgent(ctx, name); errors.Is(err, runner.ErrAgentGone) {
			return name
		}
	}
	return ""
}

// PauseIfAgentsGone pauses a running run whose builder or reviewer the host
// says is gone. It is a pause, not a stop, so nothing is freed. It writes only
// under the writer lease, which a Tick in the same process has just renewed;
// without it nothing is changed. Recover resumes the run once both are back.
func (c *Controller) PauseIfAgentsGone(ctx context.Context, runID string) (bool, error) {
	run, cfg, err := c.load(ctx, runID)
	if err != nil || run.State != "running" || c.fence(run.CheckoutID) == 0 {
		return false, err
	}
	if c.goneAgent(ctx, cfg) == "" {
		return false, nil
	}
	return true, c.d.State.SetRunState(ctx, runID, "paused", pauseAgentExited)
}
