package controller

// Reading a pane review's reply.
//
// In manual review delivery the reviewer answers in its pane. Once the pane has
// settled, the controller reads the reply, from the CLI's own log when it can
// (review_reply_log.go) and from the pane otherwise, and records it through
// RecordReviewRaw, with the same validation and bindings as a manual record. A
// reply that is missing, malformed or for another job is read again later.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	obs, err := c.settledPrompt(ctx, cfg, prompt.ID)
	if err != nil {
		return nil
	}
	jobs, err := c.d.State.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil {
		return err
	}
	kind, name := cfg.agentFor(*prompt)
	texts := replyTexts{
		// The CLI's own log holds the reply whole; the pane may show only its end.
		func(jobID string) string { return c.loggedReplies(ctx, run, kind, obs.Session, jobID) },
		// A pane that cannot be read now is read on the next pass.
		func(string) string { text, _ := pr.ReadAgent(ctx, name, paneReviewLines); return text },
	}
	for _, job := range jobs {
		if job.State != "running" {
			continue
		}
		raw, broken := texts.reply(job.ID)
		if broken != nil {
			// A reply that is not JSON is told to the reviewer now, rather
			// than left to the review timeout.
			reason := fmt.Sprintf("%v: review result: %v", workflow.ErrIncompleteEvidence, broken)
			if err := c.recordReviewRejection(ctx, run, cfg, att.ID, job.ID, prompt.ID, sha256Hex(raw), reason); err != nil {
				return err
			}
			continue
		}
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

// replyTexts are where a reviewer's reply is looked for, in order. Each is
// given the job the reply must name.
type replyTexts []func(jobID string) string

// reply is the job's reply from the first place that holds one, whole or
// broken, as lastReviewObject finds it.
func (r replyTexts) reply(jobID string) ([]byte, error) {
	for _, text := range r {
		if raw, broken := lastReviewObject(text(jobID), jobID); raw != nil || broken != nil {
			return raw, broken
		}
	}
	return nil, nil
}

// loggedReplies is what the reviewer replied, from its CLI's own log, or ""
// when the log cannot be found or read. The review prompt carries the job id,
// so that finds a conversation Herdr does not name.
func (c *Controller) loggedReplies(ctx context.Context, run state.Run, kind, session, jobID string) string {
	rr, ok := c.d.UsageSource.(ReplyReader)
	if !ok {
		return ""
	}
	created, err := c.d.State.RunCreatedMS(ctx, run.ID)
	if err != nil {
		return ""
	}
	text, _ := rr.AgentReplies(ctx, kind, session, jobID, time.UnixMilli(created), c.d.Now())
	return text
}

// lastReviewObject is the reviewer's last reply in text: the last JSON object
// that names jobID, or nil. Surrounding text is skipped.
//
// A reply that names the job but is not JSON, as when a finding quotes text
// without escaping its quotes, comes back as an error that says where it
// breaks, with the reply's bytes up to that point. That holds only when the
// broken reply comes after every reply that decodes.
//
// A CLI may wrap long lines and indent the rest (Codex does), so the text is
// searched with line breaks and the following indent removed. Ids and hashes
// survive exactly; a finding may lose a space at a wrap, so the stored bytes
// are the normalized object.
//
// The prompt's own example names this job too, but its status is the list of
// choices, so it is never taken for a reply.
func lastReviewObject(text, jobID string) ([]byte, error) {
	if !strings.Contains(text, jobID) {
		return nil, nil
	}
	text = wrapBreak.ReplaceAllString(text, "")
	found, end := scanObjects(text, jobID)
	named := regexp.MustCompile(`"job_id"\s*:\s*"`+regexp.QuoteMeta(jobID)+`"`).FindAllStringIndex(text, -1)
	if len(named) == 0 {
		return found, nil
	}
	at := named[len(named)-1][0]
	start := strings.LastIndexByte(text[:at], '{')
	if at < end || start < 0 || strings.ContainsRune(text[start:at], '}') {
		return found, nil
	}
	reply := text[start:]
	var raw json.RawMessage
	err := json.NewDecoder(strings.NewReader(reply)).Decode(&raw)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		// It decodes, so it is the prompt's example, or it runs past the
		// end of what was read and is not a whole reply.
		return found, nil
	}
	return []byte(reply[:min(int(syntax.Offset), len(reply))]), workflow.JSONError([]byte(reply), err)
}

var wrapBreak = regexp.MustCompile(`\r?\n[ \t]*`)

// scanObjects is the last JSON object in text that names jobID, and the index
// just past it.
func scanObjects(text, jobID string) ([]byte, int) {
	var found []byte
	end := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		if raw := decodeObject(text[i:], jobID); raw != nil {
			found, end = raw, i+len(raw)
			i += len(raw) - 1
		}
	}
	return found, end
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
