package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

func (c *Controller) captureStart(ctx context.Context, cfg runConfig) (string, error) {
	m, err := snapshot.Capture(ctx, cfg.Root, c.d.Artifacts)
	if err != nil {
		return "", err
	}
	if !m.Complete {
		return "", fmt.Errorf("%w: the checkout cannot be captured exactly: %s", workflow.ErrBusy, strings.Join(m.Errors, "; "))
	}
	return m.Hash, c.d.State.AdoptArtifact(ctx, m.Hash)
}

func (c *Controller) patchText(ctx context.Context, att workflow.Attempt) (string, error) {
	raw, err := c.d.Artifacts.ReadAll(ctx, att.PatchHash, 256<<20)
	if err != nil {
		return "", err
	}
	body, err := c.renderedPatch(ctx, raw)
	if len(body) > 16<<10 {
		body = append(body[:16<<10], []byte("\n[patch truncated here; the full patch is artifact "+att.PatchHash+"]\n")...)
	}
	return string(body), err
}

// patchBody is the attempt's patch as text, cut to maxPacketPatchBytes with
// the cut reported rather than hidden.
func (c *Controller) patchBody(ctx context.Context, att workflow.Attempt) (string, bool, error) {
	raw, err := c.d.Artifacts.ReadAll(ctx, att.PatchHash, 256<<20)
	if err != nil {
		return "", false, err
	}
	body, err := c.renderedPatch(ctx, raw)
	if err != nil {
		return "", false, err
	}
	if len(body) > maxPacketPatchBytes {
		return string(body[:maxPacketPatchBytes]), true, nil
	}
	return string(body), false, nil
}

// renderedPatch renders a stored patch document as text.
func (c *Controller) renderedPatch(ctx context.Context, raw []byte) ([]byte, error) {
	var doc struct {
		StartSnapshotHash string `json:"start_snapshot_hash"`
		EndSnapshotHash   string `json:"end_snapshot_hash"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	start, err := snapshot.LoadManifest(ctx, c.d.Artifacts, doc.StartSnapshotHash)
	if err != nil {
		return nil, err
	}
	end, err := snapshot.LoadManifest(ctx, c.d.Artifacts, doc.EndSnapshotHash)
	if err != nil {
		return nil, err
	}
	patch, err := snapshot.Diff(start, end)
	if err != nil {
		return nil, err
	}
	textHash, err := snapshot.RenderPatch(ctx, c.d.Artifacts, patch)
	if err != nil {
		return nil, err
	}
	return c.d.Artifacts.ReadAll(ctx, textHash, 4<<20)
}

// maxReviewBytes bounds a review result.
const maxReviewBytes = 1 << 20

// reviewIdentity records who reviewed and how much is known about them. A pane
// review is run by an agent in a Herdr pane; nothing attests that it is a
// separate or isolated reviewer, and the record says so instead of implying it.
type reviewIdentity struct {
	Kind      string `json:"kind"`
	Mode      string `json:"mode"`
	Session   string `json:"session"`
	Assurance string `json:"assurance"`
	Note      string `json:"note"`
}

// RecordReview records a pane review's structured result. See RecordReviewRaw.
func (c *Controller) RecordReview(ctx context.Context, runID string, result workflow.ReviewResult) (workflow.Receipt, error) {
	return c.RecordReviewRaw(ctx, runID, encode(result))
}

// RecordReviewRaw records a pane review from the exact bytes the reviewer handed
// back, only when:
//   - the bytes are one strict JSON result naming the open reviewer job, its
//     attempt and the patch and tree that job was given;
//   - the attempt is current, its review prompt was delivered, and its pane has
//     finished;
//   - the checkout still holds the reviewed tree. Otherwise the result is
//     refused and the run pauses.
//
// The raw bytes are stored first. It never accepts a task: the next Tick does.
func (c *Controller) RecordReviewRaw(ctx context.Context, runID string, raw []byte) (workflow.Receipt, error) {
	// A result for the independent verification stage takes the same gates as
	// one that arrived by mailbox: bound to the current patch and tree, and
	// counted only if its reviewer is not the writer.
	if c.stageOf(ctx, raw) == state.StageIndependentVerification {
		return c.recordManualVerification(ctx, runID, raw)
	}
	receipt, err := c.recordReview(ctx, runID, raw, c.paneCompletion)
	if err != nil {
		return receipt, err
	}
	// A manually recorded result answers the prompt a review_unknown pause waits
	// on, however late, so it clears that pause. A staged result clears it in its
	// own fenced write (review_reconcile.go).
	return receipt, c.clearReviewPause(ctx, runID, raw)
}

// clearReviewPause clears a review_unknown pause that waits on the review the
// validated result raw answers.
func (c *Controller) clearReviewPause(ctx context.Context, runID string, raw []byte) error {
	var result workflow.ReviewResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	run, _, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	att, err := c.d.State.GetAttempt(ctx, result.AttemptID)
	if err != nil {
		return err
	}
	return c.resolveReviewPrompts(ctx, run, att, result.JobID, "acknowledged")
}

// completion establishes that the reviewer is finished and says what is known
// about it. A pane review's evidence is its settled pane; a direct review's is
// the exit of a process the controller started and watched.
type completion func(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (reviewIdentity, error)

func (c *Controller) paneCompletion(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (reviewIdentity, error) {
	obs, err := c.reviewerFinished(ctx, run, cfg, att)
	if err != nil {
		return reviewIdentity{}, err
	}
	id := reviewIdentity{Kind: cfg.ReviewerKind, Mode: "pane", Session: obs.ExternalID, Assurance: "session_observed",
		Note: "a pane review is an agent in a Herdr pane that finished its turn and handed back this result; it is not provider-attested isolation"}
	if obs.ExternalID == "" {
		id.Assurance = "session_unavailable"
	}
	return id, nil
}

// errTreeChanged marks a refusal because the checkout is no longer the tree that
// was reviewed. The run has already been paused for it.
var errTreeChanged = errors.New("the tree changed after it was reviewed")

func (c *Controller) recordReview(ctx context.Context, runID string, raw []byte, done completion) (workflow.Receipt, error) {
	if len(raw) > maxReviewBytes {
		return workflow.Receipt{}, fmt.Errorf("%w: review result: larger than %d bytes", workflow.ErrIncompleteEvidence, maxReviewBytes)
	}
	var result workflow.ReviewResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return workflow.Receipt{}, fmt.Errorf("%w: review result: %v", workflow.ErrIncompleteEvidence, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return workflow.Receipt{}, fmt.Errorf("%w: review result: expected one JSON object and nothing after it", workflow.ErrIncompleteEvidence)
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
		return workflow.Receipt{}, fmt.Errorf("%w: review job %s does not exist", workflow.ErrIncompleteEvidence, result.JobID)
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
	id, err := done(ctx, run, cfg, att)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if err := c.treeStillReviewed(ctx, run, cfg, job.TreeHash); err != nil {
		return workflow.Receipt{}, err
	}

	resultHash, err := c.put(ctx, string(raw))
	if err != nil {
		return workflow.Receipt{}, err
	}
	identityHash, err := c.putJSON(ctx, id)
	if err != nil {
		return workflow.Receipt{}, err
	}
	rec := state.ReviewRecord{JobID: job.ID, AttemptID: att.ID, ResultHash: resultHash, PatchHash: job.PatchHash, TreeHash: job.TreeHash,
		Status: result.Status, IdentityHash: identityHash}
	// A reviewer pane's review is the independent check, so who it came from counts.
	rec.DistinctFromWriter = c.reviewerDistinct(ctx, run, cfg, id.Session)
	if err := c.d.State.RecordReviewResult(ctx, rec); err != nil {
		// A retry finds its own result already stored.
		if !errors.Is(err, workflow.ErrConflict) {
			return workflow.Receipt{}, err
		}
	}
	receipt, err := c.apply(ctx, run, cfg, att, derive(job.ID, "review-completed"), workflow.EventReviewCompleted, resultHash)
	if err != nil {
		return receipt, err
	}
	// The result is the answer to the review prompt, if there was one: it
	// resolves that effect, so a prompt that was answered never reads as an
	// unconfirmed delivery.
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return receipt, err
	}
	for _, e := range effects {
		if e.Kind == effectReviewPrompt && e.AttemptID == att.ID && e.State != "acknowledged" && e.State != "cancelled" && e.State != "failed" {
			if err := c.d.State.AcknowledgeEffect(ctx, e.ID, e.Nonce); err != nil {
				return receipt, err
			}
		}
	}
	return receipt, c.d.State.ClearReviewRejection(ctx, job.ID)
}

// resolveReviewPrompts settles every open review prompt of the attempt as to,
// and clears the run's review_unknown pause when it waits on job, or, with job
// "", on any of those prompts' jobs (state.ResolveReviewPrompt).
func (c *Controller) resolveReviewPrompts(ctx context.Context, run state.Run, att workflow.Attempt, job, to string) error {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, e := range effects {
		if e.Kind != effectReviewPrompt || e.AttemptID != att.ID {
			continue
		}
		pauseJob := job
		if pauseJob == "" {
			pauseJob = e.JobID
		}
		if _, err := c.d.State.ResolveReviewPrompt(ctx, run.ID, pauseJob, e.ID, to); err != nil {
			return err
		}
	}
	return nil
}

// Waive records the planner's decision to overrule one requirement of a task's
// current attempt, for the tree it examined, with a stated reason. Only a
// review waiver changes what acceptance needs; the others stay facts the
// acceptance check reads.
func (c *Controller) Waive(ctx context.Context, runID, taskID, scope, reason, actor string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a waiver needs a reason", workflow.ErrInvalidTransition)
	}
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	attempts, err := c.d.State.TaskAttempts(ctx, runID, taskID)
	if err != nil {
		return err
	}
	var att *workflow.Attempt
	for i := range attempts {
		if attempts[i].Current {
			att = &attempts[i]
		}
	}
	if att == nil || att.EndSnapshotHash == "" {
		return fmt.Errorf("%w: task %s has no submitted attempt to waive for", workflow.ErrIncompleteEvidence, taskID)
	}
	reasonHash, err := c.put(ctx, reason+"\n")
	if err != nil {
		return err
	}
	id := derive(att.ID, "waiver", scope, reasonHash)
	if err := c.d.State.RecordWaiver(ctx, state.Waiver{ID: id, AttemptID: att.ID, Scope: scope, Actor: actor, ReasonHash: reasonHash, EvidenceTreeHash: att.EndSnapshotHash}); err != nil {
		if !errors.Is(err, workflow.ErrConflict) {
			return err
		}
	}
	if _, err := c.apply(ctx, run, cfg, *att, derive(id, "waive"), workflow.EventWaiveRequested, reasonHash); err != nil {
		return err
	}
	if scope != "review" {
		return nil
	}
	// A waived review is not waited for: its prompts are moot, and a
	// review_unknown pause waiting on one of them has nothing left to wait for.
	if err := c.resolveReviewPrompts(ctx, run, *att, "", "cancelled"); err != nil {
		return err
	}
	return c.d.State.CancelReviewJobs(ctx, att.ID)
}

// mailboxDeps builds the ingestion dependencies for a run, with the
// controller's own evidence capture and observed facts.
func (c *Controller) mailboxDeps(run state.Run, cfg runConfig) MailboxDeps {
	return MailboxDeps{State: c.d.State, Artifacts: c.d.Artifacts, Dir: RunDir(cfg.relay(), run.ID), RunID: run.ID,
		ReviewProtocol: cfg.ReviewProtocol, ReviewDeadline: c.d.ReviewTimeout,
		ReviewRejected: func(ctx context.Context, env Envelope, reason string) error {
			eff, err := c.d.State.GetEffect(ctx, env.EffectID)
			if err != nil || eff.Nonce != env.Nonce {
				return nil
			}
			return c.recordReviewRejection(ctx, run, cfg, env.AttemptID, env.JobID, env.EffectID, env.PayloadHash, reason)
		},
		ObserveSession: func(ctx context.Context, effectID string) string {
			eff, err := c.d.State.GetEffect(ctx, effectID)
			if err != nil {
				return ""
			}
			if o, err := c.observeEffect(ctx, cfg, eff); err == nil {
				return o.ExternalID
			}
			return ""
		},
		Prepare: func(ctx context.Context, env Envelope, att workflow.Attempt) error {
			if env.Kind != workflow.EventBuilderSubmitted {
				return nil
			}
			if _, err := c.captureEvidence(ctx, cfg, att); err != nil {
				return err
			}
			return c.recordBuilderSession(ctx, cfg, env)
		},
		StillWorking: func(ctx context.Context, env Envelope) bool {
			o, err := c.d.Transport.Observe(ctx, env.EffectID)
			return err == nil && o.State == runner.ObsRunning
		},
		Observed: func(ctx context.Context, env Envelope) workflow.Observed {
			obs := c.observed(ctx, run, cfg)
			if o, err := c.d.Transport.Observe(ctx, env.EffectID); err == nil && o.State == runner.ObsCompleted {
				obs.SessionSettled = true
			}
			return obs
		},
	}
}

// Ingest applies one builder envelope to its run. A submission is applied only
// after the controller has captured the end snapshot and patch itself.
func (c *Controller) Ingest(ctx context.Context, envelope []byte) (workflow.Receipt, error) {
	env, err := ParseEnvelope(envelope)
	if err != nil {
		return workflow.Receipt{}, err
	}
	run, cfg, err := c.load(ctx, env.RunID)
	if err != nil {
		return workflow.Receipt{}, reject("wrong_run", "run %s is not known here", env.RunID)
	}
	return IngestMailbox(ctx, c.mailboxDeps(run, cfg), envelope)
}
