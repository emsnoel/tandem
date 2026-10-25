package controller

// Direct reviews: where the runner has a verified way to run a kind's CLI to
// completion, the controller runs the review itself and records its output. No
// kind is verified yet, so every review runs in a pane; this path is tested
// with fixture CLIs.
//
// A direct review that goes wrong is a failed review, never a pass: a nonzero
// exit, output that is not a result bound to its job, a timeout or too much
// output is recorded as failed with the reason. A cancelled one records
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	defaultReviewTimeout = 5 * time.Minute
	directRunnerKind     = "direct-review"
	paneRunnerKind       = "pane-review"
)

// reviewLaunch returns the launch arguments a direct review is given. By default
// they are what the adapter resolves for the autonomy recorded for the reviewer,
// unchanged. Where that is unknown the settings cannot be preserved, and an
// error means the review stays in a pane.
func (c *Controller) reviewLaunch(cfg runConfig) ([]string, error) {
	if c.d.ReviewLaunch != nil {
		return c.d.ReviewLaunch(cfg.ReviewerKind)
	}
	rec, err := config.LoadReviewer(cfg.relay())
	if err != nil {
		return nil, fmt.Errorf("the reviewer's autonomy is not recorded: %w", err)
	}
	if rec.Autonomy == "" || rec.Kind != cfg.ReviewerKind {
		return nil, errors.New("the reviewer's recorded identity is incomplete")
	}
	a, err := agent.Get(cfg.ReviewerKind)
	if err != nil {
		return nil, err
	}
	l, err := a.Resolve(agent.Spec{Kind: cfg.ReviewerKind, Role: agent.RoleReviewer, Autonomy: agent.Autonomy(rec.Autonomy)})
	if err != nil {
		return nil, err
	}
	return l.Args, nil
}

// reviewMode is where a new review runs: directly only when the runner says the
// kind and installed version are verified and the reviewer's settings are known.
func (c *Controller) reviewMode(cfg runConfig) string {
	// Direct handback needs a reviewer that can send mailbox messages: a pane.
	if cfg.ReviewProtocol >= 3 || c.d.Reviewer.Mode(cfg.ReviewerKind) != runner.ModeDirect {
		return paneRunnerKind
	}
	if _, err := c.reviewLaunch(cfg); err != nil {
		return paneRunnerKind
	}
	return directRunnerKind
}

// runDirectReviews runs at most one direct review that is waiting, and records
// its outcome. It reports whether it did anything.
func (c *Controller) runDirectReviews(ctx context.Context, run state.Run, cfg runConfig) (bool, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if t.AcceptedAttemptID != "" {
			continue
		}
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return false, err
		}
		for _, att := range attempts {
			if !att.Current || att.Implementation != workflow.ImplementationSubmitted ||
				(att.Check != workflow.CheckPassed && att.Check != workflow.CheckNotApplicable) ||
				(att.Review != workflow.ReviewPending && att.Review != workflow.ReviewRunning) {
				continue
			}
			jobs, err := c.d.State.AttemptJobs(ctx, att.ID, "reviewer")
			if err != nil {
				return false, err
			}
			for _, job := range jobs {
				if job.RunnerKind == directRunnerKind && job.State == "running" {
					return true, c.runDirect(ctx, run, cfg, t, att, job)
				}
			}
		}
	}
	return false, nil
}

func (c *Controller) runDirect(ctx context.Context, run state.Run, cfg runConfig, task state.TaskRow, att workflow.Attempt, job workflow.Job) error {
	prompt, err := c.reviewPromptText(ctx, run, cfg, task, att, job.ID, "")
	if err != nil {
		return err
	}
	launch, err := c.reviewLaunch(cfg)
	if err != nil {
		return c.failDirect(ctx, run, att, job, "", fmt.Sprintf("the reviewer's launch settings cannot be resolved: %v", err))
	}
	timeout := c.d.ReviewTimeout
	if timeout <= 0 {
		timeout = defaultReviewTimeout
	}
	// Registration comes first: a reviewer that cannot be accounted for is
	// not launched.
	if err := c.registerDirectReview(ctx, run, cfg, att, job); held(err) {
		return c.d.State.SetRunState(ctx, run.ID, "paused", workflow.PauseScheduling)
	} else if err != nil {
		return err
	}
	out, err := c.d.Reviewer.Run(ctx, runner.DirectRequest{Kind: cfg.ReviewerKind, JobID: job.ID, Launch: launch, Prompt: prompt, Timeout: timeout})
	if err != nil {
		return c.failDirect(ctx, run, att, job, out.Handle, fmt.Sprintf("the direct reviewer could not run: %v", err))
	}
	switch {
	case out.Cancelled:
		return fmt.Errorf("direct review of attempt %s was cancelled: %w", att.ID, context.Canceled)
	case out.TimedOut:
		return c.failDirect(ctx, run, att, job, out.Handle, fmt.Sprintf("the reviewer timed out after %s", timeout))
	case out.Truncated:
		return c.failDirect(ctx, run, att, job, out.Handle, fmt.Sprintf("the reviewer's output exceeded %d bytes", runner.MaxDirectOutput))
	case out.ExitCode != 0:
		return c.failDirect(ctx, run, att, job, out.Handle, fmt.Sprintf("the reviewer exited %d: %s", out.ExitCode, tailOf(out.Stderr, 300)))
	}
	_, err = c.recordReview(ctx, run.ID, []byte(strings.TrimSpace(string(out.Stdout))), directCompletion(out.Handle))
	if errors.Is(err, errTreeChanged) || !errors.Is(err, workflow.ErrIncompleteEvidence) {
		return err
	}
	return c.failDirect(ctx, run, att, job, out.Handle, fmt.Sprintf("the reviewer's output is not a valid review result: %v", err))
}

func directCompletion(handle string) completion {
	return func(_ context.Context, _ state.Run, cfg runConfig, _ workflow.Attempt) (reviewIdentity, error) {
		return reviewIdentity{Kind: cfg.ReviewerKind, Mode: "direct", Session: handle, Assurance: "process_exit_observed",
			Note: "a separate process the controller started in an empty directory and watched to its exit; it is not provider-attested isolation"}, nil
	}
}

// failDirect records a failed review for the job, with the reason as its one
// finding, so the run pauses and the failure is on record.
func (c *Controller) failDirect(ctx context.Context, run state.Run, att workflow.Attempt, job workflow.Job, handle, reason string) error {
	res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: job.PatchHash, TreeHash: job.TreeHash, Status: workflow.ReviewStatusFailed,
		Findings: []workflow.Finding{{Severity: workflow.SeverityInfo, Path: ".", Disposition: workflow.DispositionOpen, Message: tailOf(reason, 1500)}}}
	_, err := c.recordReview(ctx, run.ID, encode(res), directCompletion(handle))
	return err
}

func tailOf(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
