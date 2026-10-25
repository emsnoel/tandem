package controller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

func (c *Controller) rejectCandidate(ctx context.Context, run state.Run, cfg runConfig, cand state.ReviewCandidate, reason string) error {
	if err := c.d.State.RejectReviewCandidate(ctx, cand.ID, reason); err != nil {
		return err
	}
	return c.recordReviewRejection(ctx, run, cfg, cand.AttemptID, cand.JobID, cand.EffectID, cand.PayloadHash, reason)
}

func (c *Controller) recordReviewRejection(ctx context.Context, run state.Run, cfg runConfig, attempt, job, effect, payload, reason string) error {
	// Only an effect owned by this run can receive feedback files or a prompt.
	eff, err := c.d.State.GetEffect(ctx, effect)
	if errors.Is(err, workflow.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if eff.RunID != run.ID || eff.AttemptID != attempt || (eff.JobID != "" && eff.JobID != job) || (eff.Kind != effectReviewPrompt && eff.Kind != effectVerifyPrompt) {
		return nil
	}
	reason = tailOf(reason, 2000)
	if err := c.d.State.RecordReviewRejection(ctx, state.ReviewRejection{RunID: run.ID, AttemptID: attempt, JobID: job, EffectID: effect, PayloadHash: payload, Reason: reason}); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(RunDir(cfg.relay(), run.ID), "review", effect), "rejected.txt", []byte(reason+"\n"))
}

// Feedback is a new turn in the same conversation. Observe it first and use
// the transport's settled-pane rule again at send, so feedback cannot queue
// behind or interrupt a reviewer that is still working.
func (c *Controller) sendReviewRejections(ctx context.Context, run state.Run, cfg runConfig) error {
	fresh, err := c.d.State.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	run = fresh
	if run.State != "running" && (run.State != "paused" || run.PauseCode != pauseReview) {
		return nil
	}
	rejections, err := c.d.State.RunReviewRejections(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, r := range rejections {
		if r.Notified {
			continue
		}
		eff, err := c.d.State.GetEffect(ctx, r.EffectID)
		if err != nil {
			return err
		}
		att, err := c.d.State.GetAttempt(ctx, r.AttemptID)
		if err != nil {
			return err
		}
		job, found, err := c.d.State.GetJob(ctx, r.JobID)
		if err != nil {
			return err
		}
		if !found || job.State != "running" {
			continue
		}
		if !att.Current {
			continue
		}
		if _, err := c.settledPrompt(ctx, cfg, eff.ID); err != nil {
			continue
		}
		kind, name := cfg.agentFor(eff)
		launchID := derive(eff.ID, "rejection-"+r.PayloadHash)
		if cfg.Collaboration != "" {
			src := sourceID(eff.ID)
			if eff.Kind == effectVerifyPrompt {
				source, err := c.plannerSource(ctx, cfg)
				if err != nil {
					if held(err) {
						continue
					}
					return err
				}
				src = source.ID
			}
			if err := c.admitSourceScoped(ctx, run, cfg, launchID, "", r.JobID, kind, src, false); err != nil {
				if held(err) {
					continue
				}
				return err
			}
		}
		message := fmt.Sprintf("Tandem rejected your review: %s. Correct the result and resend for this review. Do not edit source files.", r.Reason)
		if cfg.ReviewProtocol >= 3 {
			dir := filepath.Join(RunDir(cfg.relay(), run.ID), "review", eff.ID)
			message = fmt.Sprintf("Tandem rejected your review: %s. Read %s, correct %s and run %s. Do not edit source files.", r.Reason, shellQuote(filepath.Join(dir, "rejected.txt")), shellQuote(filepath.Join(dir, "verdict.json")), shellQuote(filepath.Join(dir, "handback")))
		}
		hash, err := c.put(ctx, strings.Join(strings.Fields(message), " "))
		if err != nil {
			return err
		}
		// Claim before typing. A crash or ambiguous socket response must not
		// repeat a paid reviewer turn after restart.
		if err := c.d.State.MarkReviewRejectionNotified(ctx, eff.ID, r.PayloadHash); err != nil {
			return err
		}
		_, err = c.d.Transport.Send(ctx, runner.Delivery{Kind: effectReviewPrompt, EffectID: launchID, AttemptID: att.ID, PromptHash: hash, AgentKind: kind, SessionID: name})
		if runner.IsNotSent(err) {
			if err := c.d.State.RetryReviewRejectionNotification(ctx, eff.ID, r.PayloadHash); err != nil {
				return err
			}
		}
	}
	return nil
}
