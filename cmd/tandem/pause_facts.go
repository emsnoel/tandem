package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// checkTailBytes is how much of a failed check's log a pause reason shows.
	checkTailBytes = 1500
	// reviewFindingsShown and findingBytes bound the review summary.
	reviewFindingsShown = 5
	findingBytes        = 300
)

// pauseFacts is the run-specific detail of a pause, taken from what the run
// already stored, or "" when the code has none or none is stored. What to do
// about it is tandem-pause-note's job. taskID is the first unaccepted task.
func pauseFacts(ctx context.Context, d *openRun, code, taskID string) (string, error) {
	switch code {
	case "recovery_blocked":
		return incidentWhy(ctx, d)
	case "check_failed", "source_mutated", "review_failed", "repairs_exhausted", "verification_identity", "verification_stale":
	default:
		return "", nil
	}
	att, err := currentAttempt(ctx, d, taskID)
	if err != nil || att.ID == "" {
		return "", err
	}
	var parts []string
	add := func(s string, err error) error {
		if s != "" {
			parts = append(parts, s)
		}
		return err
	}
	switch code {
	case "check_failed", "source_mutated":
		err = add(checkTail(ctx, d, att))
	case "review_failed":
		err = add(reviewSummary(ctx, d, att))
	case "repairs_exhausted":
		if err = add(checkTail(ctx, d, att)); err == nil {
			err = add(reviewSummary(ctx, d, att))
		}
	default:
		parts = append(parts, fmt.Sprintf("The review result is for attempt %d of %s.", att.Ordinal, taskID))
	}
	return strings.Join(parts, "\n"), err
}

func currentAttempt(ctx context.Context, d *openRun, taskID string) (workflow.Attempt, error) {
	if taskID == "" {
		return workflow.Attempt{}, nil
	}
	attempts, err := d.st.TaskAttempts(ctx, d.run.RunID, taskID)
	if err != nil {
		return workflow.Attempt{}, err
	}
	for _, a := range attempts {
		if a.Current {
			return a, nil
		}
	}
	return workflow.Attempt{}, nil
}

// incidentWhy is the reason of the run's newest recovery incident.
func incidentWhy(ctx context.Context, d *openRun) (string, error) {
	incidents, err := d.st.RunIncidents(ctx, d.run.RunID)
	if err != nil || len(incidents) == 0 {
		return "", err
	}
	return "Why recovery stopped: " + incidents[len(incidents)-1].Reason, nil
}

// checkTail is the end of the attempt's latest check log.
func checkTail(ctx context.Context, d *openRun, att workflow.Attempt) (string, error) {
	cert, ok, err := d.st.AttemptCheckCertificate(ctx, att.ID)
	if err != nil || !ok {
		return "", err
	}
	head, err := d.art.Read(ctx, cert.LogHash, 0, 1)
	if err != nil {
		return "", err
	}
	off := head.TotalBytes - checkTailBytes
	if off < 0 {
		off = 0
	}
	c, err := d.art.Read(ctx, cert.LogHash, off, checkTailBytes)
	if err != nil {
		return "", err
	}
	text := string(c.Data)
	if off > 0 { // start on a whole line
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return "", nil
	}
	return fmt.Sprintf("The check exited %d. The end of its log:\n%s", cert.ExitCode, text), nil
}

// reviewSummary is the status and first findings of the attempt's latest review.
func reviewSummary(ctx context.Context, d *openRun, att workflow.Attempt) (string, error) {
	jobs, err := d.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil {
		return "", err
	}
	for i := len(jobs) - 1; i >= 0; i-- {
		if jobs[i].ResultHash == "" {
			continue
		}
		raw, err := d.art.ReadAll(ctx, jobs[i].ResultHash, 1<<20)
		if err != nil {
			return "", err
		}
		var r workflow.ReviewResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "The reviewer's last result for attempt %d: %s, %d findings.", att.Ordinal, r.Status, len(r.Findings))
		for n, f := range r.Findings {
			if n == reviewFindingsShown {
				fmt.Fprintf(&b, "\n(%d more not shown)", len(r.Findings)-n)
				break
			}
			msg := f.Message
			if len(msg) > findingBytes {
				msg = msg[:findingBytes] + "..."
			}
			fmt.Fprintf(&b, "\n- %s %s: %s", f.Severity, f.Path, msg)
		}
		return b.String(), nil
	}
	return "", nil
}
