package controller

// The planner's independent verification.
//
// When the checks and the reviewer pane's review have passed and the policy
// requires it, the controller sends the planner a verification packet in its
// original context. The answer is staged by the mailbox and applied once the
// planner settles, under the same fences as a review.
//
// The result must be bound to this attempt's patch and tree, from a verifier
// distinct from the writer; a result from the writer's own session counts as
// nothing. The stage is reserved against the planner's budget.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	effectVerifyPrompt = "verify_prompt"

	pauseVerificationIdentity = "verification_identity"
	pauseVerificationStale    = "verification_stale"

	verifierRunnerKind = "planner-verification"
)

// errNoPlannerSource: the collaboration has no single registered planner
// source, so the verification turn could not be counted. It holds the launch
// instead of letting a turn go uncounted.
var errNoPlannerSource = errors.New("the collaboration has no registered planner source to count the verification turn")

// plannerSource is the collaboration's planner: exactly one open planner source.
func (c *Controller) plannerSource(ctx context.Context, cfg runConfig) (state.Source, error) {
	sources, err := c.d.State.ListSources(ctx, cfg.Collaboration)
	if err != nil {
		return state.Source{}, err
	}
	var found []state.Source
	for _, s := range sources {
		if s.Role == "planner" && s.Status != "abandoned" {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return state.Source{}, fmt.Errorf("%w: %d planner sources are registered", errNoPlannerSource, len(found))
	}
	return found[0], nil
}

// ensureVerification creates the attempt's verification job and sends the
// planner its packet, once. It reports whether it created anything.
func (c *Controller) ensureVerification(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (bool, error) {
	if _, ok, err := c.policy(ctx, cfg); err != nil || !ok {
		return false, err
	}
	jobID := derive(att.ID, "verification-job")
	_, found, err := c.d.State.GetJob(ctx, jobID)
	if err != nil {
		return false, err
	}
	if !found {
		if err := c.d.State.InsertJob(ctx, workflow.Job{ID: jobID, AttemptID: att.ID, Kind: "reviewer", State: "running", InputHash: att.PatchHash,
			PatchHash: att.PatchHash, TreeHash: att.EndSnapshotHash, ConfigHash: run.ConfigHash, RunnerKind: verifierRunnerKind}); err != nil {
			return false, err
		}
	}
	if err := c.d.State.RegisterReviewStage(ctx, jobID, state.StageIndependentVerification); err != nil {
		return false, err
	}
	parent, err := c.reviewDispatchOf(ctx, run, att)
	if err != nil {
		return false, err
	}
	effID, nonce, err := c.d.State.DerivedEffectIdentity(ctx, parent.ID, effectVerifyPrompt)
	if err != nil {
		return false, err
	}
	if existing, err := c.d.State.GetEffect(ctx, effID); err == nil && existing.Kind == effectVerifyPrompt {
		return false, c.d.State.BindEffectJob(ctx, effID, jobID)
	}
	task, err := c.taskOf(ctx, run, att)
	if err != nil {
		return false, err
	}
	pkt, err := c.buildReviewPacket(ctx, run, cfg, task, att, jobID, stageIndependentVerification)
	if err != nil {
		return false, err
	}
	pkt.EffectID, pkt.Nonce = effID, nonce
	if err := prepareReviewHandback(cfg, &pkt); err != nil {
		return false, err
	}
	prompt, err := c.put(ctx, renderReviewPacket(pkt, cfg.ReviewProtocol))
	if err != nil {
		return false, err
	}
	derived, err := c.d.State.InsertDerivedEffect(ctx, parent.ID, effectVerifyPrompt, prompt)
	if err != nil {
		return false, err
	}
	if derived.ID != effID || derived.Nonce != nonce {
		return false, fmt.Errorf("the verification prompt was written for effect %s but stored as %s", effID, derived.ID)
	}
	return true, c.d.State.BindEffectJob(ctx, derived.ID, jobID)
}

// reviewDispatchOf is the attempt's review dispatch effect: the root the
// attempt's review prompts derive from.
func (c *Controller) reviewDispatchOf(ctx context.Context, run state.Run, att workflow.Attempt) (workflow.Effect, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return workflow.Effect{}, err
	}
	for _, e := range effects {
		if e.Kind == workflow.EffectReviewDispatch && e.AttemptID == att.ID {
			return e, nil
		}
	}
	return workflow.Effect{}, fmt.Errorf("%w: attempt %s has no review dispatch", workflow.ErrIncompleteEvidence, att.ID)
}

// verifierDistinct says whether the verifier is not the writer. The writer is
// the builder's configured session and the conversation its dispatch reached;
// the verifier is the planner's configured session and, when the transport
// saw one, the conversation the verification prompt reached. Sharing any of
// them is a self-review.
func (c *Controller) verifierDistinct(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt, observed string) bool {
	if cfg.PlannerSession == "" || cfg.PlannerSession == cfg.BuilderSession {
		return false
	}
	if observed == "" {
		return true // nothing contradicts the configuration
	}
	return !c.writerSessions(ctx, run, cfg)[observed]
}

// verificationIdentity is who verified and how much is known about it.
func verificationIdentity(cfg runConfig, mode, session, assurance string) reviewIdentity {
	return reviewIdentity{Kind: cfg.PlannerKind, Mode: mode, Session: session, Assurance: assurance,
		Note: "the planner's independent verification, in its original context; not provider-attested isolation"}
}

// recordVerification validates and records an independent verification
// result. It does not move the attempt: the attempt's review is the builder's,
// and verification is a separate fact that acceptance reads. observed is the
// conversation the verification prompt reached, when known.
func (c *Controller) recordVerification(ctx context.Context, runID string, raw []byte, mode, observed string) (workflow.Receipt, error) {
	if len(raw) > maxReviewBytes {
		return workflow.Receipt{}, fmt.Errorf("%w: verification result: larger than %d bytes", workflow.ErrIncompleteEvidence, maxReviewBytes)
	}
	var result workflow.ReviewResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return workflow.Receipt{}, fmt.Errorf("%w: verification result: %v", workflow.ErrIncompleteEvidence, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return workflow.Receipt{}, fmt.Errorf("%w: verification result: expected one JSON object and nothing after it", workflow.ErrIncompleteEvidence)
	}
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return workflow.Receipt{}, err
	}
	job, found, err := c.d.State.GetJob(ctx, result.JobID)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if !found {
		return workflow.Receipt{}, fmt.Errorf("%w: verification job %s does not exist", workflow.ErrIncompleteEvidence, result.JobID)
	}
	if stage, _, err := c.d.State.ReviewStage(ctx, job.ID); err != nil || stage != state.StageIndependentVerification {
		return workflow.Receipt{}, errors.Join(err, fmt.Errorf("%w: job %s is not an independent verification", workflow.ErrIncompleteEvidence, job.ID))
	}
	att, err := c.d.State.GetAttempt(ctx, job.AttemptID)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if att.RunID != run.ID || !att.Current {
		return workflow.Receipt{}, fmt.Errorf("%w: attempt %s is not a current attempt of run %s", workflow.ErrStaleAttempt, att.ID, run.ID)
	}
	if err := workflow.ValidateReview(result, job); err != nil {
		return workflow.Receipt{}, err
	}
	if err := c.treeStillReviewed(ctx, run, cfg, job.TreeHash); err != nil {
		return workflow.Receipt{}, err
	}
	distinct := c.verifierDistinct(ctx, run, cfg, att, observed)
	assurance := "session_observed"
	if observed == "" {
		assurance = "session_unavailable"
		if mode == "manual" {
			assurance = "operator_recorded"
		}
	}
	resultHash, err := c.put(ctx, string(raw))
	if err != nil {
		return workflow.Receipt{}, err
	}
	identityHash, err := c.putJSON(ctx, verificationIdentity(cfg, mode, observed, assurance))
	if err != nil {
		return workflow.Receipt{}, err
	}
	err = c.d.State.RecordReviewResult(ctx, state.ReviewRecord{JobID: job.ID, AttemptID: att.ID, ResultHash: resultHash, PatchHash: job.PatchHash,
		TreeHash: job.TreeHash, Status: result.Status, IdentityHash: identityHash, DistinctFromWriter: distinct})
	if err != nil && !errors.Is(err, workflow.ErrConflict) {
		return workflow.Receipt{}, err // a retry finds its own result already stored
	}
	return workflow.Receipt{EventID: derive(job.ID, "verification-recorded")}, c.d.State.ClearReviewRejection(ctx, job.ID)
}

// stageOf reports the stage of the reviewer job a result bytes names, or ""
// when it names none.
func (c *Controller) stageOf(ctx context.Context, raw []byte) string {
	var head struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(raw, &head) != nil || head.JobID == "" {
		return ""
	}
	stage, _, _ := c.d.State.ReviewStage(ctx, head.JobID)
	return stage
}

// manualVerifierSession is the conversation a manually recorded verification
// came from: unknown, so only the configuration speaks for it.
const manualVerifierSession = ""

// recordManualVerification records a verification handed over by the operator
// through review-record. The planner's pane need not be settled: the operator
// is the planner, speaking in its own turn. Nothing observes the conversation,
// so only the configuration can show the verifier is not the writer, and the
// identity says it was recorded by an operator.
func (c *Controller) recordManualVerification(ctx context.Context, runID string, raw []byte) (workflow.Receipt, error) {
	rec, err := c.recordVerification(ctx, runID, raw, "manual", manualVerifierSession)
	if err != nil {
		return rec, err
	}
	// The result answers the verification prompt, if one was sent.
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return rec, err
	}
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return rec, err
	}
	for _, e := range effects {
		if e.Kind == effectVerifyPrompt && e.State != "acknowledged" && e.State != "cancelled" && e.State != "failed" {
			if err := c.d.State.AcknowledgeEffect(ctx, e.ID, e.Nonce); err != nil {
				return rec, err
			}
		}
	}
	return rec, nil
}
