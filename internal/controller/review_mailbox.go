package controller

// Mailbox v3: how a reviewer reports to a durable run.
//
// A reviewer sends review_started when it begins and review_result with its
// review JSON, through the builder's queue under schema_version 3. Ingestion
// only stages: review_started binds the session and sets a deadline, and the
// result is stored as an immutable candidate. The controller applies a
// candidate after the reviewer settles. Stage, effect, nonce and attempt come
// from state, never from the message.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// currentAttempt finds the envelope's attempt and checks that it is the
// current attempt of the run, for the task the envelope names.
func currentAttempt(ctx context.Context, deps MailboxDeps, env Envelope) (workflow.Attempt, error) {
	attempt, err := deps.State.GetAttempt(ctx, env.AttemptID)
	switch {
	case errors.Is(err, workflow.ErrConflict):
		return attempt, rejectWith("stale_attempt", workflow.ErrStaleAttempt, "attempt %s does not exist", env.AttemptID)
	case err != nil:
		return attempt, err
	case attempt.RunID != env.RunID:
		return attempt, rejectWith("stale_attempt", workflow.ErrStaleAttempt, "attempt %s belongs to another run", env.AttemptID)
	case !attempt.Current:
		return attempt, rejectWith("stale_attempt", workflow.ErrStaleAttempt, "attempt %s was superseded", env.AttemptID)
	case attempt.TaskID != env.TaskID:
		return attempt, reject("wrong_task", "attempt %s is for task %s, not %s", env.AttemptID, attempt.TaskID, env.TaskID)
	}
	return attempt, nil
}

// ingestReviewer handles one version 3 message. Its outcomes are the same for
// any order of arrival: a result before its start is staged and acknowledges
// the prompt, and a start after the result is recorded and changes nothing.
func ingestReviewer(ctx context.Context, deps MailboxDeps, env Envelope) (receipt workflow.Receipt, err error) {
	defer func() {
		if err != nil && deps.ReviewRejected != nil {
			if _, permanent := permanent(err); permanent {
				err = errors.Join(err, deps.ReviewRejected(ctx, env, err.Error()))
			}
		}
	}()
	if deps.ReviewProtocol < 3 {
		return workflow.Receipt{}, reject("unsupported_protocol", "this run speaks review protocol %d; a version 3 message needs it opted in at start", deps.ReviewProtocol)
	}
	if !reviewerKinds[env.Kind] {
		return workflow.Receipt{}, reject("unsupported_kind", "a version 3 message is review_started or review_result, not %q", env.Kind)
	}
	attempt, err := currentAttempt(ctx, deps, env)
	if err != nil {
		return workflow.Receipt{}, err
	}
	eff, err := deps.State.GetEffect(ctx, env.EffectID)
	switch {
	case errors.Is(err, workflow.ErrConflict):
		return workflow.Receipt{}, reject("unknown_effect", "effect %s does not exist", env.EffectID)
	case err != nil:
		return workflow.Receipt{}, err
	case eff.AttemptID != attempt.ID || eff.RunID != env.RunID || (eff.Kind != effectReviewPrompt && eff.Kind != effectVerifyPrompt):
		return workflow.Receipt{}, reject("unknown_effect", "effect %s is not this attempt's review or verification prompt", env.EffectID)
	case eff.Nonce != env.Nonce:
		return workflow.Receipt{}, reject("wrong_nonce", "the nonce does not belong to effect %s", env.EffectID)
	case eff.JobID != env.JobID:
		return workflow.Receipt{}, reject("unknown_job", "effect %s does not ask for job %s", env.EffectID, env.JobID)
	}
	job, found, err := deps.State.GetJob(ctx, env.JobID)
	switch {
	case err != nil:
		return workflow.Receipt{}, err
	case !found || job.AttemptID != attempt.ID || job.Kind != "reviewer":
		return workflow.Receipt{}, reject("unknown_job", "job %s is not this attempt's reviewer job", env.JobID)
	}
	stage, registered, err := deps.State.ReviewStage(ctx, env.JobID)
	if err != nil {
		return workflow.Receipt{}, err
	} else if !registered {
		return workflow.Receipt{}, reject("unregistered_reviewer", "job %s serves no registered review stage", env.JobID)
	}
	// A stage is asked for by its own prompt: a builder review prompt cannot
	// carry an independent verification, nor the reverse.
	if (stage == state.StageIndependentVerification) != (eff.Kind == effectVerifyPrompt) {
		return workflow.Receipt{}, reject("unknown_effect", "effect %s is a %s and cannot carry the %s stage", env.EffectID, eff.Kind, stage)
	}
	body, err := readPayload(deps, env)
	if err != nil {
		return workflow.Receipt{}, err
	}
	receipt = workflow.Receipt{EventID: env.EventID, Version: attempt.Version}
	if env.Kind == EventReviewStarted {
		receipt.Duplicate, err = recordStart(ctx, deps, env, eff, body)
	} else {
		receipt.Duplicate, err = stageResult(ctx, deps, env, body)
	}
	if err != nil {
		return workflow.Receipt{}, err
	}
	// The nonce is valid for this effect, so the prompt was read. A result that
	// arrives after the effect was cancelled or failed is still staged: that
	// effect is simply not waiting for an acknowledgement any more.
	if err := deps.State.AcknowledgeEffect(ctx, eff.ID, env.Nonce); err != nil && !errors.Is(err, workflow.ErrConflict) {
		return workflow.Receipt{}, err
	}
	return receipt, nil
}

// decodeBody decodes exactly one JSON value from body.
func decodeBody(body []byte, v any, strict bool) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("the payload is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON value")
	}
	return nil
}

// recordStart stores a verified start. The payload is a small object that may
// name the session the reviewer runs in; the transport's own observation of
// the effect, when there is one, must agree with it.
func recordStart(ctx context.Context, deps MailboxDeps, env Envelope, eff workflow.Effect, body []byte) (bool, error) {
	var start struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeBody(body, &start, true); err != nil {
		return false, reject("malformed_start", "the start payload is not {\"session_id\": ...}: %v", err)
	}
	if start.SessionID != "" && !sessionPattern.MatchString(start.SessionID) {
		return false, reject("malformed_start", "session_id is not a plain session name")
	}
	observed := ""
	if deps.ObserveSession != nil {
		observed = deps.ObserveSession(ctx, eff.ID)
	}
	if observed != "" && start.SessionID != "" && observed != start.SessionID {
		return false, reject("session_mismatch", "the reviewer says session %s; the transport saw %s", start.SessionID, observed)
	}
	deadline := deps.ReviewDeadline
	if deadline <= 0 {
		deadline = defaultReviewTimeout
	}
	_, dup, err := deps.State.RecordReviewStart(ctx, state.ReviewStartRequest{JobID: env.JobID, EffectID: eff.ID, EventID: env.EventID,
		SessionID: start.SessionID, ObservedSession: observed, Deadline: deadline})
	if errors.Is(err, workflow.ErrConflict) {
		return false, rejectWith("conflicting_start", err, "%v", err)
	}
	return dup, err
}

// stageResult stores the result's exact bytes as an immutable candidate. The
// bytes must be one JSON object; whether it is a valid review is decided when
// the candidate is applied, by the same validator a manual review-record uses.
func stageResult(ctx context.Context, deps MailboxDeps, env Envelope, body []byte) (bool, error) {
	var probe map[string]json.RawMessage
	if err := decodeBody(body, &probe, false); err != nil || probe == nil {
		return false, reject("malformed_result", "the result is not one JSON object: %v", err)
	}
	if err := workflow.ValidateVerdictRaw(body); err != nil {
		return false, reject("malformed_result", "%v", err)
	}
	job, found, err := deps.State.GetJob(ctx, env.JobID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, reject("unknown_job", "the reviewer job is gone")
	}
	if _, err := workflow.DecodeReview(body, job); err != nil {
		return false, reject("malformed_result", "%v", err)
	}
	hash, err := storePayload(ctx, deps, body)
	if err != nil {
		return false, err
	}
	_, dup, err := deps.State.StageReviewCandidate(ctx, state.ReviewCandidateRequest{RunID: env.RunID, AttemptID: env.AttemptID,
		JobID: env.JobID, EffectID: env.EffectID, EventID: env.EventID, PayloadHash: hash})
	if errors.Is(err, workflow.ErrConflict) {
		return false, rejectWith("conflicting_candidate", err, "%v", err)
	}
	return dup, err
}
