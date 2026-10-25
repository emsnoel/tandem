package controller

// Reading a pane review's reply.
//
// In manual review delivery the reviewer answers in its pane. Once the pane has
// settled, the controller reads the reply and records it through
// RecordReviewRaw, with the same validation and bindings as a manual record. A
// reply that is missing, malformed or for another job is read again later.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// paneReviewLines is how much of the reviewer's pane is searched for the reply.
const paneReviewLines = 400

// readPaneReviews records the replies of the run's open pane reviews.
func (c *Controller) readPaneReviews(ctx context.Context, run state.Run, cfg runConfig) error {
	pr, ok := c.d.Transport.(runner.PaneReader)
	if !ok || cfg.ReviewProtocol >= 3 || (run.State != "running" && run.State != "paused") {
		return nil
	}
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	var first error
	for _, t := range tasks {
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return err
		}
		for _, att := range attempts {
			if !att.Current || (att.Review != workflow.ReviewPending && att.Review != workflow.ReviewRunning) {
				continue
			}
			if err := c.readPaneReview(ctx, pr, run, cfg, att); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (c *Controller) readPaneReview(ctx context.Context, pr runner.PaneReader, run state.Run, cfg runConfig, att workflow.Attempt) error {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return err
	}
	var prompt *workflow.Effect
	for i := range effects {
		if e := effects[i]; e.Kind == effectReviewPrompt && e.AttemptID == att.ID {
			prompt = &effects[i]
		}
	}
	if prompt == nil {
		return nil
	}
	// A prompt not yet delivered, or a reviewer still working, has no finished
	// reply. RecordReviewRaw repeats the check; reading a working pane is waste.
	if _, err := c.settledPrompt(ctx, cfg, prompt.ID); err != nil {
		return nil
	}
	jobs, err := c.d.State.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil {
		return err
	}
	_, name := cfg.agentFor(*prompt)
	var text string
	for _, job := range jobs {
		if job.State != "running" {
			continue
		}
		if text == "" {
			if text, err = pr.ReadAgent(ctx, name, paneReviewLines); err != nil {
				return nil // a pane that cannot be read now is read on the next pass
			}
		}
		raw := lastReviewObject(text, job.ID)
		if raw == nil {
			continue
		}
		if _, err := c.RecordReviewRaw(ctx, run.ID, raw); err != nil {
			if errors.Is(err, workflow.ErrIncompleteEvidence) || errors.Is(err, workflow.ErrStaleAttempt) {
				if err := c.recordReviewRejection(ctx, run, cfg, att.ID, job.ID, prompt.ID, sha256Hex(raw), err.Error()); err != nil {
					return err
				}
				continue
			}
			return err
		}
	}
	return nil
}

// lastReviewObject is the last JSON object in text that names jobID, or nil.
// Surrounding text is skipped.
//
// A CLI may wrap long lines and indent the rest (Codex does), so the text is
// searched with line breaks and the following indent removed. Ids and hashes
// survive exactly; a finding may lose a space at a wrap, so the stored bytes
// are the normalized object.
//
// The prompt's own example names this job too, but its status is the list of
// choices, so it is never taken for a reply.
func lastReviewObject(text, jobID string) []byte {
	if !strings.Contains(text, jobID) {
		return nil
	}
	return scanObjects(wrapBreak.ReplaceAllString(text, ""), jobID)
}

var wrapBreak = regexp.MustCompile(`\r?\n[ \t]*`)

// scanObjects is the last JSON object in text that names jobID.
func scanObjects(text, jobID string) []byte {
	var found []byte
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		if raw := decodeObject(text[i:], jobID); raw != nil {
			found = raw
			i += len(raw) - 1
		}
	}
	return found
}

// decodeObject is the JSON object s starts with, when it names jobID and is
// not the prompt's example.
func decodeObject(s, jobID string) []byte {
	var raw json.RawMessage
	if err := json.NewDecoder(strings.NewReader(s)).Decode(&raw); err != nil {
		return nil
	}
	var obj struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &obj) != nil || obj.JobID != jobID || strings.Contains(obj.Status, "|") {
		return nil
	}
	return append([]byte(nil), raw...)
}
