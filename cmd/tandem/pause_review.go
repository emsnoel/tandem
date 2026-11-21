package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// reviewIdentity is what a reply to a review_unknown pause's review must name,
// as the review prompt gave it, and where the reviewer's CLI logged its reply.
// A person who records the reply by hand needs both: a pane can show only the
// end of a reply. It is "" when the review's job cannot be found.
func reviewIdentity(ctx context.Context, d *openRun, run state.Run, reviewerKind, taskID string) string {
	job, ok := pausedReviewJob(ctx, d, run, taskID)
	if !ok {
		return ""
	}
	lines := []string{"The reply must name these, as the review prompt gave them:",
		"job_id: " + job.ID, "attempt_id: " + job.AttemptID, "patch_hash: " + job.PatchHash, "tree_hash: " + job.TreeHash}
	if where := replyLog(reviewerKind); where != "" {
		lines = append(lines, "The reviewer's whole reply is in its "+where+".")
	}
	return strings.Join(lines, "\n")
}

// pausedReviewJob is the reviewer job the pause waits on, or, when the pause
// names none, the current attempt's latest one.
func pausedReviewJob(ctx context.Context, d *openRun, run state.Run, taskID string) (workflow.Job, bool) {
	if run.PauseJobID != "" {
		job, found, err := d.st.GetJob(ctx, run.PauseJobID)
		return job, err == nil && found
	}
	att, err := currentAttempt(ctx, d, taskID)
	if err != nil || att.ID == "" {
		return workflow.Job{}, false
	}
	jobs, err := d.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil || len(jobs) == 0 {
		return workflow.Job{}, false
	}
	return jobs[len(jobs)-1], true
}

// replyLog names where a reviewer kind's CLI logs its conversations.
func replyLog(kind string) string {
	switch kind {
	case "codex":
		return fmt.Sprintf("Codex session log, a rollout-*.jsonl file under %s", controller.DefaultCodexSessions())
	case "claude":
		return fmt.Sprintf("Claude Code conversation log, a .jsonl file under %s", controller.DefaultClaudeProjects())
	case "copilot":
		return fmt.Sprintf("Copilot conversation log, an events.jsonl file under %s", filepath.Join(controller.DefaultCopilotHome(), "session-state"))
	case "agy":
		return "conversation files under ~/.gemini/antigravity-cli/conversations; tandem can't read those files yet"
	}
	return ""
}
