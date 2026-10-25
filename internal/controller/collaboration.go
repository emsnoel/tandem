package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// A run with a collaboration registers every model job as an expected source
// before the launch is sent, and binds the session its receipt reports.
// Without a collaboration nothing here runs.
//
// A source id is derived from the effect or job, so a retried send reuses its
// registration. An empty receipt binds nothing, and collection reports the
// source as incomplete.

// launchRole is the source role of a model job an effect starts, or "" for an
// effect that launches nothing new (a planner notice, a cancel).
func launchRole(kind string) string {
	switch kind {
	case workflow.EffectBuilderDispatch:
		return "builder"
	case effectReviewPrompt:
		return "reviewer"
	}
	return ""
}

// sourceID names the expected source launched by an effect or a direct job.
func sourceID(launchID string) string { return derive("source", launchID) }

// requireCollaboration checks, at run start, that the collaboration exists and
// takes launches.
func (c *Controller) requireCollaboration(ctx context.Context, id string) error {
	status, err := c.d.State.CollaborationStatus(ctx, id)
	if err != nil {
		return err
	}
	if status != "open" {
		return fmt.Errorf("%w: collaboration %s is %s and takes no new runs", workflow.ErrInvalidTransition, id, status)
	}
	return nil
}

// admissionID names the admission of an effect or a direct job.
func admissionID(launchID string) string { return derive("admission", launchID) }

// registerLaunch is the Deliverer's BeforeSend for a collaboration run: it
// registers the launch's source and, when the collaboration enforces budgets,
// admits it. Either failing sends nothing.
func (c *Controller) registerLaunch(run state.Run, cfg runConfig) func(context.Context, workflow.Effect, runner.Delivery) error {
	return func(ctx context.Context, eff workflow.Effect, d runner.Delivery) error {
		if eff.Kind == effectVerifyPrompt {
			if err := c.admitVerification(ctx, run, cfg, eff); held(err) {
				return runner.Hold(err)
			} else {
				return err
			}
		}
		if eff.Kind == effectRecoveryDecision {
			if err := c.admitDecision(ctx, run, cfg, eff); held(err) {
				return runner.Hold(err)
			} else {
				return err
			}
		}
		role := launchRole(eff.Kind)
		if role == "" {
			return nil
		}
		err := c.d.State.RegisterSource(ctx, state.SourceRequest{ID: sourceID(eff.ID), CollaborationID: cfg.Collaboration,
			Provider: d.AgentKind, Role: role, RunID: run.ID, AttemptID: eff.AttemptID, JobID: eff.JobID, StartBoundary: eff.ID})
		if err == nil {
			err = c.admit(ctx, run, cfg, eff.ID, eff.AttemptID, eff.JobID, d.AgentKind)
		}
		if held(err) {
			return runner.Hold(err)
		}
		return err
	}
}

// held reports a refusal that holds a launch rather than failing it: the
// collaboration is stopped, or its budget cannot admit the launch now.
func held(err error) bool {
	return errors.Is(err, state.ErrStopped) || errors.Is(err, state.ErrBudgetExhausted) || errors.Is(err, state.ErrBudgetUnconfigured) ||
		errors.Is(err, errNoPlannerSource)
}

// admitVerification admits the planner's verification turn. The turn happens
// inside the planner's registered session, so it is reserved against and
// counted with that source; a collaboration with no single registered planner
// holds the launch rather than let the turn go uncounted.
func (c *Controller) admitVerification(ctx context.Context, run state.Run, cfg runConfig, eff workflow.Effect) error {
	policy, ok, err := c.policy(ctx, cfg)
	if err != nil || !ok {
		return err
	}
	stage, _ := policy.Stage(stageIndependentVerification)
	src, err := c.plannerSource(ctx, cfg)
	if err != nil {
		return err
	}
	return c.admitSource(ctx, run, cfg, eff.ID, eff.AttemptID, eff.JobID, stage.Provider, src.ID)
}

// ResumeHeld resumes a run that was paused because a launch was held, once its
// collaboration has been resumed or its budget amended. The held launch is
// admitted again on the next tick, or the run pauses again. A run paused for
// any other reason is left as it is.
func (c *Controller) ResumeHeld(ctx context.Context, runID string) error {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.State != "paused" || run.PauseCode != workflow.PauseScheduling {
		return fmt.Errorf("%w: run %s is %s (%s), not held for scheduling", workflow.ErrInvalidTransition, runID, run.State, run.PauseCode)
	}
	return c.d.State.SetRunState(ctx, runID, "running", "")
}

// admit passes a launch through dispatch admission when the collaboration
// enforces budgets. It reads the run's version, the stop generation and the
// lease fence it holds now; a change between that read and the admission's
// transaction refuses the launch, which is retried with fresh values.
func (c *Controller) admit(ctx context.Context, run state.Run, cfg runConfig, launchID, attemptID, jobID, provider string) error {
	return c.admitSource(ctx, run, cfg, launchID, attemptID, jobID, provider, sourceID(launchID))
}

// admitSource is admit against a named source, for a turn that is counted
// with a source registered earlier.
func (c *Controller) admitSource(ctx context.Context, run state.Run, cfg runConfig, launchID, attemptID, jobID, provider, srcID string) error {
	return c.admitSourceScoped(ctx, run, cfg, launchID, attemptID, jobID, provider, srcID, true)
}

// admitSourceScoped is admitSource, optionally without the run's fences. A
// planner turn sent while the run is paused is still reserved against the
// planner's budget and refused after a stop, but there is no running run, run
// version or lease for it to hold.
func (c *Controller) admitSourceScoped(ctx context.Context, run state.Run, cfg runConfig, launchID, attemptID, jobID, provider, srcID string, runScoped bool) error {
	enforced, err := c.d.State.BudgetEnforced(ctx, cfg.Collaboration)
	if err != nil || !enforced {
		return err
	}
	budget, err := c.d.State.ProviderBudget(ctx, cfg.Collaboration, provider)
	if err != nil {
		return err
	}
	var in, out int64
	if !budget.Unlimited {
		if budget.InputReservation == nil || budget.OutputReservation == nil {
			return fmt.Errorf("%w: %s declares no per-launch reservation", state.ErrBudgetUnconfigured, provider)
		}
		in, out = *budget.InputReservation, *budget.OutputReservation
	}
	current, err := c.d.State.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	gen, err := c.d.State.StopGeneration(ctx, cfg.Collaboration)
	if err != nil {
		return err
	}
	req := state.AdmissionRequest{ID: admissionID(launchID), CollaborationID: cfg.Collaboration,
		RunID: run.ID, AttemptID: attemptID, JobID: jobID, EffectID: launchID, SourceID: srcID, Provider: provider,
		RunVersion: current.Version, StopGeneration: gen, LeaseFence: c.fence(run.CheckoutID), InputReservation: in, OutputReservation: out}
	if !runScoped {
		req.RunID, req.RunVersion, req.LeaseFence = "", 0, 0
	}
	_, err = c.d.State.AdmitDispatch(ctx, req)
	return err
}

// releaseLaunch returns the reservation of an effect that failed its last
// permitted send, so certainly delivered nothing.
func (c *Controller) releaseLaunch(ctx context.Context, cfg runConfig, eff workflow.Effect) error {
	if cfg.Collaboration == "" || (launchRole(eff.Kind) == "" && eff.Kind != effectVerifyPrompt && eff.Kind != effectRecoveryDecision) {
		return nil
	}
	err := c.d.State.ReleaseAdmission(ctx, admissionID(eff.ID))
	if errors.Is(err, state.ErrNoSource) {
		return nil // never admitted: the collaboration does not enforce budgets
	}
	return err
}

// bindLaunch records the session a sent effect's receipt reported.
func (c *Controller) bindLaunch(ctx context.Context, cfg runConfig, eff workflow.Effect) error {
	if cfg.Collaboration == "" || launchRole(eff.Kind) == "" {
		return nil
	}
	session := c.sessionOfEffect(ctx, eff)
	if session == "" {
		return nil
	}
	return c.d.State.BindSourceSession(ctx, state.BindRequest{SourceID: sourceID(eff.ID), SessionID: session})
}

// registerDirectReview registers a direct reviewer before its process starts.
// The process handle is not a provider session, so the source stays
// unresolved until collection can establish one.
func (c *Controller) registerDirectReview(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt, job workflow.Job) error {
	if cfg.Collaboration == "" {
		return nil
	}
	if err := c.d.State.RegisterSource(ctx, state.SourceRequest{ID: sourceID(job.ID), CollaborationID: cfg.Collaboration,
		Provider: cfg.ReviewerKind, Role: "reviewer", RunID: run.ID, AttemptID: att.ID, JobID: job.ID, StartBoundary: job.ID}); err != nil {
		return err
	}
	return c.admit(ctx, run, cfg, job.ID, att.ID, job.ID, cfg.ReviewerKind)
}
