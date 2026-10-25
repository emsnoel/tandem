package controller

// Applying a reviewer's staged result.
//
// The controller turns a staged candidate into the review outcome once the
// reviewer is settled and the checkout still holds the reviewed tree, and then
// clears a review_unknown pause only if it waits on this reviewer. The order:
//
//  1. read the fence: run version, pause and stop generations, the lease, the
//     attempt, the pause cause and the reviewed tree;
//  2. observe the settled reviewer and verify the tree on disk;
//  3. record the review outcome, as a manual review-record does;
//  4. apply the candidate in one guarded transaction that rechecks the fence.
//
// If anything in the fence changed by step 4, it is a no-op and the candidate
// stays staged. A paused run reconciles but schedules nothing in the same pass;
// a stopped run is not reconciled.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// errReviewerNotSettled marks a candidate that has to wait: its reviewer is not
// observed to have finished. It is not a refusal.
var errReviewerNotSettled = errors.New("the reviewer has not settled")

// reconcileReviews applies the run's staged candidates and notices reviews
// that started and never submitted.
func (c *Controller) reconcileReviews(ctx context.Context, run state.Run, cfg runConfig) error {
	if cfg.ReviewProtocol < 3 || (run.State != "running" && run.State != "paused") {
		return nil
	}
	cands, err := c.d.State.StagedReviewCandidates(ctx, run.ID)
	if err != nil {
		return err
	}
	var first error
	for _, cand := range cands {
		if err := c.applyCandidate(ctx, run, cfg, cand); err != nil && first == nil {
			first = err
		}
	}
	if run, err = c.d.State.GetRun(ctx, run.ID); err != nil {
		return errors.Join(first, err)
	}
	return errors.Join(first, c.pauseStalledReviews(ctx, run))
}

func (c *Controller) applyCandidate(ctx context.Context, run state.Run, cfg runConfig, cand state.ReviewCandidate) error {
	att, err := c.d.State.GetAttempt(ctx, cand.AttemptID)
	if err != nil {
		return err
	}
	job, found, err := c.d.State.GetJob(ctx, cand.JobID)
	if err != nil || !found {
		return errors.Join(err, c.rejectCandidate(ctx, run, cfg, cand, "the reviewer job is gone"))
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, cand.PayloadHash, maxReviewBytes+1)
	if err != nil {
		return err
	}
	result, err := workflow.DecodeReview(raw, job)
	if err != nil {
		return c.rejectCandidate(ctx, run, cfg, cand, err.Error())
	}
	// Keep the exact submitted artifact, and record a separate bound result.
	// Replay compares the bound bytes, since verdict-only bytes have no identity.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, full := fields["job_id"]; !full {
		raw = encode(result)
	}
	// An outcome recorded from these very bytes is this candidate's own, left
	// by an application that stopped before its guarded write: finish it. Any
	// other outcome (a manual review-record, say) is not.
	recorded, hasRecorded, err := c.d.State.RecordedReviewResult(ctx, job.ID)
	if err != nil {
		return err
	}
	ours := hasRecorded && recorded == sha256Hex(raw)
	verifying := cand.Stage == state.StageIndependentVerification
	switch {
	case !att.Current:
		return c.rejectCandidate(ctx, run, cfg, cand, "stale_attempt: the attempt was superseded before the result was applied")
	case hasRecorded && !ours:
		return c.rejectCandidate(ctx, run, cfg, cand, "the job already has a different recorded result")
	case !verifying && !ours && att.Review != workflow.ReviewPending && att.Review != workflow.ReviewRunning:
		return c.rejectCandidate(ctx, run, cfg, cand, "the attempt already has a review outcome ("+string(att.Review)+")")
	}

	// 1. The fence, read before anything is observed.
	fence, err := c.d.State.CurrentReviewFence(ctx, run.ID, att.ID)
	if err != nil {
		return err
	}
	fence.LeaseFence = c.fence(run.CheckoutID) // the lease this controller believes it holds
	if fence.TreeHash != job.TreeHash {
		return c.rejectCandidate(ctx, run, cfg, cand, "the reviewed tree is not the attempt's tree")
	}
	if err := c.hook(c.hooks.afterReviewFence); err != nil {
		return err
	}

	// 2 and 3. Settled reviewer, tree on disk, then the outcome itself.
	if verifying {
		_, err = c.recordCandidateVerification(ctx, run, cfg, cand, raw)
	} else {
		_, err = c.recordReview(ctx, run.ID, raw, c.candidateCompletion(cand))
	}
	switch {
	case errors.Is(err, errReviewerNotSettled):
		return nil // wait: bytes arriving are not a settled reviewer
	case errors.Is(err, errTreeChanged):
		return c.rejectCandidate(ctx, run, cfg, cand, "the tree changed after it was reviewed")
	case errors.Is(err, workflow.ErrIncompleteEvidence), errors.Is(err, workflow.ErrStaleAttempt):
		return c.rejectCandidate(ctx, run, cfg, cand, err.Error())
	case errors.Is(err, workflow.ErrConflict), errors.Is(err, workflow.ErrInvalidTransition):
		return nil // the attempt moved under it; the next pass reads again
	case err != nil:
		return err
	}
	if err := c.hook(c.hooks.beforeCandidateApply); err != nil {
		return err
	}

	// 4. The guarded write.
	res, err := c.d.State.ApplyReviewCandidate(ctx, state.ReviewApplyRequest{CandidateID: cand.ID, RunID: run.ID, AttemptID: att.ID,
		JobID: job.ID, EffectID: cand.EffectID, PatchHash: job.PatchHash, TreeHash: fence.TreeHash, PauseReason: fence.PauseReason,
		PauseJobID: fence.PauseJobID, RunVersion: fence.RunVersion, PauseGeneration: fence.PauseGeneration,
		StopGeneration: fence.StopGeneration, LeaseFence: fence.LeaseFence})
	if err != nil {
		return err
	}
	if !res.Applied {
		return nil // the fence moved: a fresh pass decides again
	}
	return c.hook(c.hooks.afterCandidateApply)
}

func (c *Controller) hook(h func() error) error {
	if h == nil {
		return nil
	}
	return h()
}

// candidateCompletion establishes that the candidate's reviewer is finished and
// says what is known about it. Its evidence is the settled session of the
// prompt that asked for the review, plus the session it named when it started.
func (c *Controller) candidateCompletion(cand state.ReviewCandidate) completion {
	return func(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (reviewIdentity, error) {
		obs, err := c.reviewerFinished(ctx, run, cfg, att)
		if err != nil {
			return reviewIdentity{}, fmt.Errorf("%w: %w", errReviewerNotSettled, err)
		}
		if err := c.hook(c.hooks.afterReviewObserved); err != nil {
			return reviewIdentity{}, err
		}
		session, assurance := obs.ExternalID, "session_observed"
		if start, found, _ := c.d.State.ReviewStartFor(ctx, cand.JobID); found && session == "" {
			session = start.SessionID
		}
		if session == "" {
			assurance = "session_unavailable"
		}
		return reviewIdentity{Kind: cfg.ReviewerKind, Mode: "mailbox", Session: session, Assurance: assurance,
			Note: "a reviewer that submitted its result by mailbox and was then observed to have finished; it is not provider-attested isolation"}, nil
	}
}

// pauseStalledReviews pauses a running run for a review that started and
// passed its execution deadline without a result. The pause waits on that
// reviewer, so its late result can clear it. A delivery that is only unknown
// has no deadline: it is the acknowledgement window's business.
func (c *Controller) pauseStalledReviews(ctx context.Context, run state.Run) error {
	if run.State != "running" {
		return nil
	}
	stalled, err := c.d.State.StalledReviews(ctx, run.ID, c.d.Now().UnixMilli())
	if err != nil || len(stalled) == 0 {
		return err
	}
	return c.d.State.SetRunPausedFor(ctx, run.ID, pauseReview, stalled[0].JobID, stalled[0].EffectID)
}

// settledPrompt checks that the prompt behind a candidate was delivered and its
// agent is now seen to have finished. A verification prompt went to the
// planner, so it is observed by the planner's name where the transport can,
// not through the builder's pane.
func (c *Controller) settledPrompt(ctx context.Context, cfg runConfig, effectID string) (runner.Observation, error) {
	eff, err := c.d.State.GetEffect(ctx, effectID)
	if err != nil {
		return runner.Observation{}, err
	}
	switch eff.State {
	case "delivering", "acknowledged", "delivery_unknown":
	default:
		return runner.Observation{}, fmt.Errorf("%w: %w: the prompt is %s", errReviewerNotSettled, workflow.ErrIncompleteEvidence, eff.State)
	}
	obs, err := c.observeEffect(ctx, cfg, eff)
	if err != nil {
		return obs, fmt.Errorf("%w: %w: the reviewer cannot be observed: %v", errReviewerNotSettled, workflow.ErrIncompleteEvidence, err)
	}
	if obs.State != runner.ObsCompleted {
		return obs, fmt.Errorf("%w: %w: observed %q", errReviewerNotSettled, workflow.ErrIncompleteEvidence, obs.State)
	}
	return obs, nil
}

// observeEffect observes the agent an effect was sent to. A verification
// prompt went to the planner, so it is observed by the planner's name where
// the transport can; asking for the effect would answer for the builder's pane.
// A review prompt sent to a reviewer pane is observed by that pane's name.
func (c *Controller) observeEffect(ctx context.Context, cfg runConfig, eff workflow.Effect) (runner.Observation, error) {
	if eff.Kind == effectReviewPrompt {
		return c.observeReviewer(ctx, cfg)
	}
	if ao, ok := c.d.Transport.(runner.AgentObserver); ok && eff.Kind == effectVerifyPrompt {
		return ao.ObserveAgent(ctx, cfg.PlannerSession)
	}
	return c.d.Transport.Observe(ctx, eff.ID)
}

// recordCandidateVerification records a staged independent verification once
// the planner's turn is seen to have finished.
func (c *Controller) recordCandidateVerification(ctx context.Context, run state.Run, cfg runConfig, cand state.ReviewCandidate, raw []byte) (workflow.Receipt, error) {
	obs, err := c.settledPrompt(ctx, cfg, cand.EffectID)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if err := c.hook(c.hooks.afterReviewObserved); err != nil {
		return workflow.Receipt{}, err
	}
	return c.recordVerification(ctx, run.ID, raw, "mailbox", obs.ExternalID)
}
