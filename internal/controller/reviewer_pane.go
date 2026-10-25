package controller

// The reviewer pane.
//
// Every run names a third agent that takes every review prompt, so the builder
// can run a cheap model and the review a strong one. It is never the builder or
// the planner. Tasks still go to the builder, and notices, verifications and
// recovery decisions to the planner. The pane is observed by its own name and
// never reset.
//
// Its passing review is the independent check. A result from the builder's or
// the planner's conversation pauses the run instead.

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// judgeAlone is what a reviewer pane is told about its own context.
const judgeAlone = "This pane keeps earlier tasks in its context. Judge this packet on its own: " +
	"what you saw or decided for an earlier task is not evidence about this one."

// ValidateReviewerPane checks a start request's reviewer pane. Start checks it
// too; a caller checks it first when it has work to do before Start.
func ValidateReviewerPane(req StartRequest) error {
	switch {
	case req.ReviewerKind == "" && req.ReviewerSession == "":
		return fmt.Errorf("%w: a run needs a reviewer pane: the builder never reviews. %s", workflow.ErrInvalidTransition, config.NoReviewerRemedy)
	case req.ReviewerKind == "" || req.ReviewerSession == "":
		return fmt.Errorf("%w: a reviewer pane needs both its kind and its agent name", workflow.ErrInvalidTransition)
	case !sessionPattern.MatchString(req.ReviewerSession):
		return fmt.Errorf("%w: %q is not an agent name", workflow.ErrInvalidTransition, req.ReviewerSession)
	case req.ReviewerSession == req.BuilderSession:
		return fmt.Errorf("%w: the reviewer pane %s is the builder: the builder would review its own work. %s", workflow.ErrInvalidTransition, req.ReviewerSession, config.NoReviewerRemedy)
	case req.ReviewerSession == req.PlannerSession:
		return fmt.Errorf("%w: the reviewer pane %s is the planner: the planner would review its own plan. %s", workflow.ErrInvalidTransition, req.ReviewerSession, config.NoReviewerRemedy)
	}
	return nil
}

// RecordedReviewerPane is the reviewer pane a run is given: req's when it names
// one, otherwise the one tandem-pair recorded in relay's reviewer.json. A
// missing or incomplete record wraps workflow.ErrConflict, and a pane that is
// the builder or the planner is refused as ValidateReviewerPane refuses it.
// Both engines' starts and an import read the reviewer through this.
func RecordedReviewerPane(relay string, req StartRequest) (kind, session string, err error) {
	if req.ReviewerKind == "" && req.ReviewerSession == "" {
		rec, err := config.LoadReviewer(relay)
		switch {
		case errors.Is(err, config.ErrNoReviewer):
			return "", "", fmt.Errorf("%w: a run needs a reviewer pane: the builder never reviews, and %v. %s", workflow.ErrConflict, err, config.NoReviewerRemedy)
		case err != nil:
			return "", "", fmt.Errorf("%w: %v. %s", workflow.ErrConflict, err, config.NoReviewerRemedy)
		}
		req.ReviewerKind, req.ReviewerSession = rec.Kind, rec.Name
	}
	if err := ValidateReviewerPane(req); err != nil {
		return "", "", err
	}
	return req.ReviewerKind, req.ReviewerSession, nil
}

// agentFor is the agent kind and session an effect is delivered to.
func (cfg runConfig) agentFor(e workflow.Effect) (string, string) {
	switch {
	case e.Kind == effectNotify || e.Kind == effectVerifyPrompt || e.Kind == effectRecoveryDecision:
		return cfg.PlannerKind, cfg.PlannerSession
	case e.Kind == effectReviewPrompt:
		return cfg.ReviewerKind, cfg.ReviewerSession
	}
	return cfg.BuilderKind, cfg.BuilderSession
}

// observeReviewer observes the reviewer pane by name. A transport that can only
// observe by effect would answer for the builder's pane, so it cannot tell.
func (c *Controller) observeReviewer(ctx context.Context, cfg runConfig) (runner.Observation, error) {
	ao, ok := c.d.Transport.(runner.AgentObserver)
	if !ok {
		return runner.Observation{}, fmt.Errorf("the transport cannot observe the reviewer pane %s by name", cfg.ReviewerSession)
	}
	return ao.ObserveAgent(ctx, cfg.ReviewerSession)
}

// writerSessions are the conversations the work came from: the builder's
// configured session and every conversation its dispatches reached.
func (c *Controller) writerSessions(ctx context.Context, run state.Run, cfg runConfig) map[string]bool {
	writers := map[string]bool{cfg.BuilderSession: true}
	if effects, err := c.d.State.RunEffects(ctx, run.ID); err == nil {
		for _, e := range effects {
			if e.Kind == workflow.EffectBuilderDispatch {
				if s := c.sessionOfEffect(ctx, e); s != "" {
					writers[s] = true
				}
			}
		}
	}
	return writers
}

// reviewerDistinct says whether the reviewer pane, in the conversation it was
// observed in, is neither the writer nor the planner. Start refused a pane
// configured as either; the observation can still show the prompt reached the
// builder's conversation or one the planner was sent to.
func (c *Controller) reviewerDistinct(ctx context.Context, run state.Run, cfg runConfig, observed string) bool {
	if cfg.ReviewerSession == "" || cfg.ReviewerSession == cfg.BuilderSession || cfg.ReviewerSession == cfg.PlannerSession {
		return false
	}
	if observed == "" {
		return true // nothing contradicts the configuration
	}
	if c.writerSessions(ctx, run, cfg)[observed] || observed == cfg.PlannerSession {
		return false
	}
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return false
	}
	for _, e := range effects {
		if _, to := cfg.agentFor(e); to == cfg.PlannerSession && c.sessionOfEffect(ctx, e) == observed {
			return false
		}
	}
	return true
}

// reviewerGate decides acceptance from the reviewer pane's review. decided is
// false when the attempt has no review from the pane (a waived review, say):
// the planner's verification then decides.
func (c *Controller) reviewerGate(ctx context.Context, run state.Run, att workflow.Attempt) (decided, proceed, progressed bool, err error) {
	v, err := c.d.State.ReviewerOutcome(ctx, att.ID)
	if err != nil || !v.Found {
		return false, false, false, err
	}
	if !v.Distinct {
		// A review from the writer's own side is recorded and counts for nothing.
		return true, false, true, c.d.State.SetRunState(ctx, run.ID, "paused", pauseVerificationIdentity)
	}
	return true, true, false, nil
}
