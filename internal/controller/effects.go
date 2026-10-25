package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// processInternal performs the run's local effects: checks, review requests,
// repair attempts and the step after an acceptance. Each is idempotent, so an
// effect whose worker died is simply done again.
func (c *Controller) processInternal(ctx context.Context, run state.Run, cfg runConfig) (bool, error) {
	progressed := false
	for i := 0; i < 20; i++ {
		kinds, err := c.gateInternal(ctx, run, cfg)
		if err != nil {
			return progressed, err
		}
		eff, err := c.d.State.ClaimEffectKinds(ctx, c.owner, run.ID, kinds)
		if errors.Is(err, state.ErrNoEffect) {
			return progressed, nil
		}
		if err != nil {
			return progressed, err
		}
		progressed = true
		var perr error
		switch eff.Kind {
		case workflow.EffectCheckDispatch:
			perr = c.runCheckEffect(ctx, run, cfg, eff)
		case workflow.EffectReviewDispatch:
			perr = c.expandReview(ctx, run, cfg, eff)
		case workflow.EffectCreateRepairAttempt:
			perr = c.createRepair(ctx, run, cfg, eff)
		case workflow.EffectAdvanceRun:
			perr = c.advanceRun(ctx, run, eff)
		}
		if perr != nil {
			return progressed, perr
		}
		if err := c.d.State.AcknowledgeEffect(ctx, eff.ID, eff.Nonce); err != nil {
			return progressed, err
		}
		fresh, err := c.d.State.GetRun(ctx, run.ID)
		if err != nil {
			return progressed, err
		}
		if fresh.State != "running" {
			return progressed, nil
		}
	}
	return progressed, nil
}

func (c *Controller) runCheckEffect(ctx context.Context, run state.Run, cfg runConfig, eff workflow.Effect) error {
	att, err := c.d.State.GetAttempt(ctx, eff.AttemptID)
	if err != nil {
		return err
	}
	if !att.Current || att.Check != workflow.CheckPending {
		return nil // superseded, or already decided: nothing to run
	}
	req, err := StartCheck(ctx, c.d.State, c.d.Artifacts, CheckSpec{AttemptID: att.ID, InputHash: att.EndSnapshotHash, Root: cfg.Root, Command: cfg.CheckCommand})
	if err != nil {
		return err
	}
	res, err := c.d.CheckExecutor(ctx, req)
	if err != nil {
		_ = c.d.State.FinishJob(context.WithoutCancel(ctx), req.JobID, "cancelled")
		return err
	}
	_, err = RecordCheck(ctx, c.d.State, c.d.Artifacts, RecordSpec{RunID: run.ID, ExpectedVersion: att.Version, EventID: derive(req.JobID, "check-completed")},
		res, c.observed(ctx, run, cfg))
	if errors.Is(err, ErrSourceMutated) {
		// Recorded, not applied: ask for a clean verification pass, never loop.
		return c.d.State.SetRunState(ctx, run.ID, "paused", pauseSourceMutated)
	}
	return err
}

// expandReview turns "a review is due" into a reviewer job and the prompt that
// asks for it.
func (c *Controller) expandReview(ctx context.Context, run state.Run, cfg runConfig, eff workflow.Effect) error {
	att, err := c.d.State.GetAttempt(ctx, eff.AttemptID)
	if err != nil {
		return err
	}
	if !att.Current {
		return nil
	}
	jobID := derive(eff.ID, "reviewer-job")
	mode := paneRunnerKind
	if job, found, err := c.d.State.GetJob(ctx, jobID); err != nil {
		return err
	} else if found {
		mode = job.RunnerKind // a repeat after a crash keeps the first decision
	} else {
		mode = c.reviewMode(cfg)
		if err := c.d.State.InsertJob(ctx, workflow.Job{ID: jobID, AttemptID: att.ID, Kind: "reviewer", State: "running",
			InputHash: att.PatchHash, PatchHash: att.PatchHash, TreeHash: att.EndSnapshotHash, ConfigHash: run.ConfigHash, RunnerKind: mode}); err != nil {
			return err
		}
	}
	// A run with direct handback registers what a reviewer's messages will be
	// checked against: the stage this job serves, before anything is sent.
	if cfg.ReviewProtocol >= 3 {
		if err := c.d.State.RegisterReviewStage(ctx, jobID, state.StageBuilderReview); err != nil {
			return err
		}
	}
	if mode == directRunnerKind {
		return nil // the controller runs it itself; no pane is asked
	}
	task, err := c.taskOf(ctx, run, att)
	if err != nil {
		return err
	}
	text, err := c.reviewPromptText(ctx, run, cfg, task, att, jobID, eff.ID)
	if err != nil {
		return err
	}
	prompt, err := c.put(ctx, text)
	if err != nil {
		return err
	}
	derived, err := c.d.State.InsertDerivedEffect(ctx, eff.ID, effectReviewPrompt, prompt)
	if err != nil || cfg.ReviewProtocol < 3 {
		return err
	}
	return c.d.State.BindEffectJob(ctx, derived.ID, jobID)
}

// reviewPromptText is the review the reviewer is asked for, for one job. A run
// with direct handback gets the full packet, naming the identity its reply
// must carry; any other run keeps the original prompt, byte for byte. parentID
// is the review dispatch the prompt derives from.
func (c *Controller) reviewPromptText(ctx context.Context, run state.Run, cfg runConfig, task state.TaskRow, att workflow.Attempt, jobID, parentID string) (string, error) {
	if cfg.ReviewProtocol >= 3 {
		pkt, err := c.buildReviewPacket(ctx, run, cfg, task, att, jobID, stageReview)
		if err != nil {
			return "", err
		}
		pkt.WarmPane = true
		if pkt.EffectID, pkt.Nonce, err = c.d.State.DerivedEffectIdentity(ctx, parentID, effectReviewPrompt); err != nil {
			return "", err
		}
		if err := prepareReviewHandback(cfg, &pkt); err != nil {
			return "", err
		}
		return renderReviewPacket(pkt, cfg.ReviewProtocol), nil
	}
	text, err := c.patchText(ctx, att)
	if err != nil {
		return "", err
	}
	return reviewPrompt(reviewPromptData{Task: task.Ordinal, Slug: task.ID, Job: jobID, Attempt: att.ID,
		PatchHash: att.PatchHash, Tree: att.EndSnapshotHash, Patch: text, WarmPane: true}), nil
}

func (c *Controller) taskOf(ctx context.Context, run state.Run, att workflow.Attempt) (state.TaskRow, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return state.TaskRow{}, err
	}
	for _, t := range tasks {
		if t.ID == att.TaskID {
			return t, nil
		}
	}
	return state.TaskRow{}, fmt.Errorf("attempt %s names task %s, which the run does not have", att.ID, att.TaskID)
}

// createRepair starts the next attempt of a task whose review asked for changes.
func (c *Controller) createRepair(ctx context.Context, run state.Run, cfg runConfig, eff workflow.Effect) error {
	parent, err := c.d.State.GetAttempt(ctx, eff.AttemptID)
	if err != nil {
		return err
	}
	attempts, err := c.d.State.TaskAttempts(ctx, run.ID, parent.TaskID)
	if err != nil {
		return err
	}
	for _, a := range attempts {
		if a.ParentAttemptID == parent.ID {
			return nil // already created before a crash
		}
	}
	task, err := c.taskOf(ctx, run, parent)
	if err != nil {
		return err
	}
	snap, err := c.captureStart(ctx, cfg)
	if err != nil {
		return err
	}
	rc, err := c.repairFor(ctx, parent)
	if err != nil {
		return err
	}
	return c.startAttempt(ctx, run, cfg, task, parent.Ordinal+1, parent.ID, snap, rc)
}

// advanceRun runs after an acceptance. The next task is started by drive; here
// the final notice is queued once every task is accepted.
func (c *Controller) advanceRun(ctx context.Context, run state.Run, eff workflow.Effect) error {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	var done []string
	for _, t := range tasks {
		if t.AcceptedAttemptID == "" {
			return nil
		}
		done = append(done, t.ID)
	}
	msg, err := c.put(ctx, fmt.Sprintf("[build] All %d tasks of the plan are accepted: %s.\nRun %s is finished. Read the work, then close the session.\n", len(done), strings.Join(done, ", "), run.ID))
	if err != nil {
		return err
	}
	_, err = c.d.State.InsertDerivedEffect(ctx, eff.ID, effectNotify, msg)
	return err
}

// deliver sends at most one external effect and reacts to what happened.
func (c *Controller) deliver(ctx context.Context, run state.Run, cfg runConfig, kinds []string) error {
	d := &runner.Deliverer{Store: c.d.State, Transport: c.d.Transport, Artifacts: c.d.Artifacts, Owner: c.owner, RunID: run.ID,
		AgentKind: cfg.BuilderKind, Kinds: kinds, Target: cfg.agentFor}
	if cfg.Collaboration != "" {
		d.BeforeSend = c.registerLaunch(run, cfg)
	}
	out, err := d.Tick(ctx)
	if out.EffectID == "" {
		return err
	}
	eff, gerr := c.d.State.GetEffect(ctx, out.EffectID)
	if gerr != nil {
		return errors.Join(err, gerr)
	}
	switch out.Kind {
	case runner.Sent:
		if berr := c.bindLaunch(ctx, cfg, eff); berr != nil {
			return errors.Join(err, berr)
		}
		// A notice, a decision request and a cancel need no acknowledgement
		// from an agent.
		if eff.Kind == effectNotify || eff.Kind == effectRecoveryDecision || eff.Kind == workflow.EffectCancel {
			if aerr := c.d.State.AcknowledgeEffect(ctx, eff.ID, eff.Nonce); aerr != nil {
				return errors.Join(err, aerr)
			}
		}
		if eff.Kind == effectNotify {
			return errors.Join(err, c.finish(ctx, run))
		}
	case runner.Unknown, runner.StillUnknown:
		switch eff.Kind {
		case workflow.EffectBuilderDispatch:
			return errors.Join(err, c.d.State.SetRunState(ctx, run.ID, "paused", pauseWriterUnknown))
		case effectReviewPrompt, effectVerifyPrompt:
			// The pause waits on this prompt's reviewer, so only its result can
			// clear it. A prompt bound to no job (manual delivery) names none.
			return errors.Join(err, c.d.State.SetRunPausedFor(ctx, run.ID, pauseReview, eff.JobID, eff.ID))
		}
	case runner.Failed:
		if eff.Kind == effectRecoveryDecision {
			// The run is already paused for this incident. Keep that reason; the
			// failed request is visible and is not sent a second time.
			return errors.Join(err, c.releaseLaunch(ctx, cfg, eff))
		}
		if eff.Kind == workflow.EffectCancel && run.State == "stopping" {
			// A builder that is gone is not a builder that stopped, so the stop is
			// not over. Pausing here ended it as dispatch_failed, and a second
			// stop could not leave that pause. The run stays stopping until a
			// builder is back and observed to have stopped.
			return errors.Join(err, c.releaseLaunch(ctx, cfg, eff))
		}
		return errors.Join(err, c.releaseLaunch(ctx, cfg, eff), c.d.State.SetRunState(ctx, run.ID, "paused", pauseDispatch))
	case runner.Held:
		if eff.Kind == effectRecoveryDecision {
			return nil // waits, counted, until the planner can take it
		}
		// Not started and not lost, like a run budget: the run pauses with the
		// effect pending, and ResumeHeld lets it try again.
		return c.d.State.SetRunState(ctx, run.ID, "paused", workflow.PauseScheduling)
	}
	return err
}

// finish ends a run whose final notice went out and releases the writer lease.
func (c *Controller) finish(ctx context.Context, run state.Run) error {
	if err := c.d.State.SetRunState(ctx, run.ID, "finished", ""); err != nil {
		return err
	}
	return c.d.State.ReleaseLease(ctx, run.CheckoutID, c.owner, c.fence(run.CheckoutID))
}
