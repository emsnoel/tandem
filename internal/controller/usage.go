package controller

// Usage for a run: what its model jobs consumed, with unknown kept apart from
// zero.
//
// The agents' own logs are the evidence. For each builder job the controller
// reads the logs of its session and everything it spawned, and stores one
// record per event under a source id that cannot repeat, so rereading counts
// nothing twice. An unreadable log leaves a marker, so the total says it is
// partial.
//
// A pane review shares the builder's session, so its tokens are in the
// builder's figure and no reviewer number is given.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Usage is token use with unknowns kept null. See workflow.Usage.
type Usage = workflow.Usage

// UsageSource finds the logs of a session and of everything descended from it.
type UsageSource interface {
	// Collect returns one stream per log found. A kind it has no way to read is
	// workflow.ErrUnsupported.
	Collect(ctx context.Context, kind, session string, from, to time.Time) ([]rollout.Stream, error)
}

// AgentUsageSource reads each builder kind's own logs: Codex rollouts under
// CodexSessions, Claude Code conversations under ClaudeProjects, Copilot
// conversations under CopilotHome. Reading only: it changes nothing
// there. A kind with no log reader (opencode) is workflow.ErrUnsupported, which
// the run records as unavailable usage.
type AgentUsageSource struct {
	CodexSessions  string
	ClaudeProjects string
	CopilotHome    string
}

// DefaultAgentUsageSource reads each CLI's logs where it files them.
func DefaultAgentUsageSource() AgentUsageSource {
	return AgentUsageSource{CodexSessions: DefaultCodexSessions(), ClaudeProjects: DefaultClaudeProjects(), CopilotHome: DefaultCopilotHome()}
}

// DefaultCopilotHome is where Copilot CLI keeps its state, logs and
// conversations: $COPILOT_HOME, or ~/.copilot.
func DefaultCopilotHome() string {
	if h := os.Getenv("COPILOT_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".copilot")
}

// DefaultCodexSessions is where Codex files its logs: $CODEX_HOME/sessions, or
// ~/.codex/sessions.
func DefaultCodexSessions() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

func (a AgentUsageSource) Collect(_ context.Context, kind, session string, from, to time.Time) ([]rollout.Stream, error) {
	switch kind {
	case "codex":
		return rollout.Collect(a.CodexSessions, session, from, to).Streams, nil
	case "claude":
		return claudeStreams(a.ClaudeProjects, session), nil
	case "copilot":
		return copilotStreams(a.CopilotHome, session), nil
	}
	return nil, fmt.Errorf("%w: usage is read from Codex, Claude and Copilot logs only; there is no source for %s", workflow.ErrUnsupported, kind)
}

// SessionFinder is a UsageSource that can also find a conversation by text in
// its log, for a builder whose session no one reported.
type SessionFinder interface {
	// SessionWith returns the conversation whose log holds marker, "" when
	// none does. A kind it cannot search is workflow.ErrUnsupported.
	SessionWith(ctx context.Context, kind, marker string, from, to time.Time) (string, error)
}

// SessionWith searches Codex and Copilot logs. Claude needs no search because
// Herdr names Claude conversations. Herdr's Copilot id is the launch id, so a
// Copilot task's conversation is found by its nonce.
func (a AgentUsageSource) SessionWith(_ context.Context, kind, marker string, from, to time.Time) (string, error) {
	switch kind {
	case "codex":
		return rollout.SessionContaining(a.CodexSessions, marker, from, to)
	case "copilot":
		return copilotSessionContaining(a.CopilotHome, marker, from, to)
	}
	return "", fmt.Errorf("%w: conversations are searched in Codex and Copilot logs only, not %s", workflow.ErrUnsupported, kind)
}

// UsageExport is one stored record with the identity an evaluation needs: where
// it came from, which provider and model, and the configuration it ran under.
type UsageExport struct {
	SourceID   string `json:"source_id"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	ConfigHash string `json:"config_hash"`
	Role       string `json:"role"`
	TaskID     string `json:"task_id"`
	AttemptID  string `json:"attempt_id"`
	JobID      string `json:"job_id"`
	Usage
}

// UsageReport is a run's usage in total and by role.
type UsageReport struct {
	RunID string `json:"run_id"`
	// Scope is always UsageScopeRun: this report counts the model jobs of one
	// run. Planning, preflight review, replacement runs and closeout are outside
	// it; the collaboration ledger (internal/usageledger) accounts for those.
	Scope  string           `json:"scope"`
	Total  Usage            `json:"total"`
	ByRole map[string]Usage `json:"by_role"`
	// Sources is how many records the total is made of.
	Sources int      `json:"sources"`
	Notes   []string `json:"notes"`
	// PlannerTurns counts the turns the run asked of the planner, by what they
	// were for. Coordination is a turn spent only to poll, forward or approve
	// an advance. A run with direct review handback asks none; the planner is
	// asked for the verification it must give and told once when the run ends.
	PlannerTurns PlannerTurns `json:"planner_turns"`
	// Records are every record, when asked for.
	Records []UsageExport `json:"records,omitempty"`
}

// PlannerTurns are the prompts a run sent to its planner.
type PlannerTurns struct {
	Verification  int `json:"verification"`
	Notifications int `json:"notifications"`
	Coordination  int `json:"coordination"`
}

// plannerTurns counts the planner-bound effects that were actually sent.
func (c *Controller) plannerTurns(ctx context.Context, runID string) (PlannerTurns, error) {
	effects, err := c.d.State.RunEffects(ctx, runID)
	if err != nil {
		return PlannerTurns{}, err
	}
	var t PlannerTurns
	for _, e := range effects {
		if e.State == "pending" || e.State == "failed" || e.State == "cancelled" {
			continue // never reached the planner
		}
		switch e.Kind {
		case effectVerifyPrompt:
			t.Verification++
		case effectNotify:
			t.Notifications++
		}
	}
	return t, nil
}

// UsageScopeRun labels a run-scoped report, narrower than a collaboration's
// totals.
const UsageScopeRun = "run_jobs"

// CollectUsage reads the logs, stores what is new and returns the run's total.
func (c *Controller) CollectUsage(ctx context.Context, runID string) (Usage, error) {
	rep, err := c.UsageReport(ctx, runID, false)
	return rep.Total, err
}

// UsageReport collects usage and reports it by role. Records are included only
// when asked for: a long run has thousands.
func (c *Controller) UsageReport(ctx context.Context, runID string, records bool) (UsageReport, error) {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return UsageReport{}, err
	}
	notes, sentTasks, err := c.collectUsage(ctx, run, cfg)
	if err != nil {
		return UsageReport{}, err
	}
	sent := len(sentTasks) > 0
	rows, err := c.d.State.ListUsage(ctx, runID)
	if err != nil {
		return UsageReport{}, err
	}
	rep := UsageReport{RunID: runID, Scope: UsageScopeRun, ByRole: map[string]Usage{}, Sources: len(rows), Notes: notes}
	byRole := map[string][]Usage{}
	for _, r := range rows {
		role := usageRole(r)
		byRole[role] = append(byRole[role], r.Usage)
		if records {
			rep.Records = append(rep.Records, UsageExport{SourceID: r.SourceID, Provider: cfg.usageProvider(role), Model: r.Model, ConfigHash: r.JobConfigHash,
				Role: role, TaskID: r.TaskID, AttemptID: r.AttemptID, JobID: r.JobID, Usage: r.Usage})
		}
	}
	for role, parts := range byRole {
		rep.ByRole[role] = workflow.SumUsage(parts)
	}
	if reviewed, err := c.paneReviewed(ctx, run); err != nil {
		return UsageReport{}, err
	} else if reviewed {
		rep.ByRole["reviewer"] = Usage{Completeness: workflow.UsageUnavailable, InputSemantics: workflow.SemanticsUnknown}
		rep.Notes = append(rep.Notes, "reviews ran in the reviewer pane "+cfg.ReviewerSession+": their tokens are not in this run figure; a collaboration counts them as reviewer sources of their own")
	}
	switch {
	case !sent:
		// No model job has been asked anything, so nothing has been used.
		zero := Usage{Input: workflow.Int64(0), CachedInput: workflow.Int64(0), Output: workflow.Int64(0), Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}
		rep.Total = zero
	default:
		all := make([]Usage, 0, len(rows))
		for _, r := range rows {
			all = append(all, r.Usage)
		}
		rep.Total = workflow.SumUsage(all)
	}
	if rep.PlannerTurns, err = c.plannerTurns(ctx, runID); err != nil {
		return UsageReport{}, err
	}
	sort.Strings(rep.Notes)
	return rep, nil
}

// usageProvider is the agent kind whose job a usage row counts, by the row's
// role (usageRole): the reviewer pane's kind for a reviewer row, the builder's
// otherwise.
func (cfg runConfig) usageProvider(role string) string {
	if role == "reviewer" {
		return cfg.ReviewerKind
	}
	return cfg.BuilderKind
}

func usageRole(r state.UsageRow) string {
	switch {
	case r.JobKind == "reviewer":
		return "reviewer"
	case r.AttemptOrdinal > 1 || r.ParentAttemptID != "":
		return "repair"
	}
	return "builder"
}

// paneReviewed reports whether any review of the run ran in a pane.
func (c *Controller) paneReviewed(ctx context.Context, run state.Run) (bool, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return false, err
		}
		for _, a := range attempts {
			jobs, err := c.d.State.AttemptJobs(ctx, a.ID, "reviewer")
			if err != nil {
				return false, err
			}
			for _, j := range jobs {
				if j.RunnerKind == paneRunnerKind {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
