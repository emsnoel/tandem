package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// checkTailBytes is how much of a failed check's log a pause reason shows.
	checkTailBytes = 1500
	// reviewFindingsShown and findingBytes bound the review summary.
	reviewFindingsShown = 5
	findingBytes        = 300
	findingPathBytes    = 200
	incidentBytes       = 500
)

// ansi matches the colour and cursor sequences a tool prints into a log.
var ansi = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\a\x1b]*(\a|\x1b\\)|[@-Z\\-_])`)

// clean keeps a fact fit for a prompt: no escape sequences, no control bytes
// except newline and tab, and only whole UTF-8 characters.
func clean(s string) string {
	s = ansi.ReplaceAllString(strings.ToValidUTF8(s, ""), "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			return r
		}
		return -1
	}, s)
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// pauseFacts is the run-specific detail of a pause, taken from what the run
// already stored, or "" when the code has none or none can be read. It never
// fails: a missing, damaged or unparsable record drops that fact, so a
// read-only status always works. What to do about a pause is
// tandem-pause-note's job. taskID is the first unaccepted task.
func pauseFacts(ctx context.Context, d *openRun, code, taskID string) string {
	switch code {
	case "recovery_blocked":
		return clean(incidentWhy(ctx, d))
	case "builder_blocked":
		return quietFact(code)
	case "check_failed", "review_failed", "repairs_exhausted", "verification_identity", "verification_stale":
	default:
		return ""
	}
	att, err := currentAttempt(ctx, d, taskID)
	if err != nil || att.ID == "" {
		return ""
	}
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	switch code {
	case "check_failed":
		add(checkTail(ctx, d, att))
	case "review_failed":
		add(reviewSummary(ctx, d, att))
	case "repairs_exhausted":
		add(checkTail(ctx, d, att))
		add(reviewSummary(ctx, d, att))
	case "verification_identity", "verification_stale":
		add(fmt.Sprintf("The review result is for attempt %d of %s.", att.Ordinal, taskID))
	}
	return clean(strings.Join(parts, "\n"))
}

// quietFact is how long the builder's pane stayed quiet before the run paused,
// in the window this process was given (TANDEM_SILENCE_WINDOW_MS can shorten it).
func quietFact(code string) string {
	how := "blocked at a dialog"
	return "The builder's pane stayed " + how + " for " + windowText(controller.SilenceWindow) + "."
}

// windowText is a duration as a person says it: whole minutes, else seconds.
func windowText(d time.Duration) string {
	unit, n := "second", int64(d.Round(time.Second)/time.Second)
	if d >= time.Minute && d%time.Minute == 0 {
		unit, n = "minute", int64(d/time.Minute)
	}
	if n < 1 {
		n = 1
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
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
func incidentWhy(ctx context.Context, d *openRun) string {
	incidents, err := d.st.RunIncidents(ctx, d.run.RunID)
	if err != nil || len(incidents) == 0 {
		return ""
	}
	return "Why recovery stopped: " + clip(clean(incidents[len(incidents)-1].Reason), incidentBytes)
}

// checkTail is the end of the attempt's latest check log.
func checkTail(ctx context.Context, d *openRun, att workflow.Attempt) string {
	cert, ok, err := d.st.AttemptCheckCertificate(ctx, att.ID)
	if err != nil || !ok {
		return ""
	}
	head, err := d.art.Read(ctx, cert.LogHash, 0, 1)
	if err != nil {
		return ""
	}
	off := head.TotalBytes - checkTailBytes
	if off < 0 {
		off = 0
	}
	c, err := d.art.Read(ctx, cert.LogHash, off, checkTailBytes)
	if err != nil {
		return ""
	}
	text := clean(string(c.Data))
	if off > 0 { // start on a whole line, unless the last line is all there is
		if i := strings.IndexByte(text, '\n'); i >= 0 && strings.TrimSpace(text[i+1:]) != "" {
			text = text[i+1:]
		} else {
			text = "..." + text
		}
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return fmt.Sprintf("The check exited %d. The end of its log:\n%s", cert.ExitCode, text)
}

// reviewSummary is the status and first findings of the attempt's latest review.
func reviewSummary(ctx context.Context, d *openRun, att workflow.Attempt) string {
	jobs, err := d.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil {
		return ""
	}
	for i := len(jobs) - 1; i >= 0; i-- {
		if jobs[i].ResultHash == "" {
			continue
		}
		raw, err := d.art.ReadAll(ctx, jobs[i].ResultHash, 1<<20)
		if err != nil {
			return ""
		}
		var r workflow.ReviewResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return ""
		}
		var b strings.Builder
		fmt.Fprintf(&b, "The reviewer's last result for attempt %d: %s, %d findings.", att.Ordinal, clip(clean(r.Status), 40), len(r.Findings))
		for n, f := range r.Findings {
			if n == reviewFindingsShown {
				fmt.Fprintf(&b, "\n(%d more not shown)", len(r.Findings)-n)
				break
			}
			fmt.Fprintf(&b, "\n- %s %s: %s", clip(clean(f.Severity), 40), clip(clean(f.Path), findingPathBytes), clip(clean(f.Message), findingBytes))
		}
		return b.String()
	}
	return ""
}
