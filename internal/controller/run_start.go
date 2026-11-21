package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Start creates a run for the plan and begins its first task. Nothing is
// created unless the checkout can be captured exactly and no other writer holds
// the checkout: the run, its tasks and the writer lease are written together.
func (c *Controller) Start(ctx context.Context, req StartRequest) (string, error) {
	// Publishing artifacts and recording the rows that name them is two steps;
	// collection must not run between them.
	locker, err := state.OpenLocker(c.d.State.Root())
	if err != nil {
		return "", err
	}
	unlock, err := locker.LockShared(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	root, common, err := state.GitIdentity(req.Root)
	if err != nil {
		return "", err
	}
	if err := validateReviewOptions(req); err != nil {
		return "", err
	}
	if err := ValidateReviewerPane(req); err != nil {
		return "", err
	}
	// Usage tandem cannot read counts as unknown, and unknown usage against a
	// limit pauses the run before every review. So a limit on a run with an
	// unmeasured kind only blocks work, and the run starts without one (#113).
	if UnmeasuredKind(req.BuilderKind, req.ReviewerKind) != "" {
		req.TaskInputLimit, req.RunInputLimit = 0, 0
	}
	if req.Collaboration != "" {
		if err := c.requireCollaboration(ctx, req.Collaboration); err != nil {
			return "", err
		}
	}
	raw, err := os.ReadFile(req.PlanPath)
	if err != nil {
		return "", err
	}
	imp := plan.Import(string(raw))
	if err := imp.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", req.PlanPath, err)
	}
	start, err := snapshot.Capture(ctx, root, c.d.Artifacts)
	if err != nil {
		return "", err
	}
	if !start.Complete {
		return "", fmt.Errorf("%w: the checkout cannot be captured exactly, so no run was started: %s", workflow.ErrIncompleteEvidence, strings.Join(start.Errors, "; "))
	}
	if err := c.d.State.AdoptArtifact(ctx, start.Hash); err != nil {
		return "", err
	}

	planHash, err := c.put(ctx, string(raw))
	if err != nil {
		return "", err
	}
	policyHash := ""
	if req.AcceptancePolicyPath != "" {
		raw, err := loadAcceptancePolicy(req.AcceptancePolicyPath)
		if err != nil {
			return "", err
		}
		if policyHash, err = c.put(ctx, string(raw)); err != nil {
			return "", err
		}
	}
	recoveryHash := ""
	if req.RecoveryPolicyPath != "" {
		raw, err := loadRecoveryPolicy(req.RecoveryPolicyPath)
		if err != nil {
			return "", err
		}
		if recoveryHash, err = c.put(ctx, string(raw)); err != nil {
			return "", err
		}
	}
	var tasks []state.TaskRow
	for i, spec := range imp.Specs {
		brief, err := c.put(ctx, imp.Briefs[i])
		if err != nil {
			return "", err
		}
		// A task's constraints are the plan's whole preamble, or a plain
		// statement that it has none; its spec records everything the row cannot.
		constraints := spec.Constraints
		if strings.TrimSpace(constraints) == "" {
			constraints = "no constraints beyond the brief"
		}
		constraintsHash, err := c.put(ctx, constraints+"\n")
		if err != nil {
			return "", err
		}
		specHash, err := c.putJSON(ctx, spec)
		if err != nil {
			return "", err
		}
		tasks = append(tasks, state.TaskRow{ID: spec.ID, Ordinal: i + 1, BriefHash: brief, ConstraintsHash: constraintsHash, SpecHash: specHash,
			DependsOn: spec.DependsOn})
	}

	resume, rc, err := c.applyResume(ctx, req, planHash, start.Hash, policyHash, tasks)
	if err != nil {
		return "", err
	}
	cfgHash, err := c.putJSON(ctx, runConfig{Version: 1, Root: root, PlanPath: req.PlanPath, BuilderKind: req.BuilderKind,
		BuilderSession: req.BuilderSession, PlannerKind: "planner", PlannerSession: req.PlannerSession, CheckCommand: req.CheckCommand,
		TaskInputLimit: req.TaskInputLimit, RunInputLimit: req.RunInputLimit, ContextLimit: req.ContextLimit,
		Collaboration: req.Collaboration, ReviewProtocol: req.ReviewProtocol, ReviewDelivery: req.ReviewDelivery,
		AcceptancePolicy: policyHash, RecoveryPolicy: recoveryHash, ReviewerKind: req.ReviewerKind, ReviewerSession: req.ReviewerSession,
		Settings: settingsFor(req), ResumedFrom: resume.From})
	if err != nil {
		return "", err
	}
	runID, err := c.d.NewID()
	if err != nil {
		return "", err
	}
	spec := state.RunSpec{RunID: runID, CheckoutID: c.d.State.CheckoutID(), CanonicalRoot: root, GitCommonDir: common,
		ManifestRoot: ".", PlanHash: planHash, ConfigHash: cfgHash, TaskInputLimit: req.TaskInputLimit, RunInputLimit: req.RunInputLimit, Tasks: tasks}
	first := firstToStart(tasks, resume.Repair)
	if first < 0 {
		return "", fmt.Errorf("%w: no task of the plan can start: each one waits on another", workflow.ErrInvalidTransition)
	}
	checkout := spec.CheckoutID
	fence, err := c.d.State.CreateRunAndLease(ctx, spec, c.owner, leaseTTL)
	if err != nil {
		return "", err
	}
	c.setFence(checkout, fence)

	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	manifest := RunManifest{RunID: runID}
	if req.ReviewProtocol >= 3 {
		manifest.ReviewProtocol = req.ReviewProtocol
	}
	for _, t := range tasks {
		manifest.Tasks = append(manifest.Tasks, ManifestTask{Ordinal: t.Ordinal, ID: t.ID})
	}
	if _, err := InitRunDir(paths.Relay(root), ActiveRun{RunID: runID, ProjectID: run.ProjectID}, manifest); err != nil {
		return runID, err
	}
	if recoveryHash != "" {
		if err := c.bindTasksToWork(ctx, runID, req.Collaboration); err != nil {
			return runID, err
		}
	}
	cfg, err := c.config(ctx, run)
	if err != nil {
		return runID, err
	}
	return runID, c.startAttempt(ctx, run, cfg, tasks[first], 1, "", start.Hash, rc)
}

// firstToStart is the task a new run starts with, or -1: the task under repair
// when a resumed run carries one, else the first task in plan order that is
// not accepted and whose dependencies all are.
func firstToStart(tasks []state.TaskRow, repair *workflow.Attempt) int {
	accepted := map[string]bool{}
	for _, t := range tasks {
		accepted[t.ID] = t.AcceptedAttemptID != ""
	}
	for i, t := range tasks {
		if repair != nil && t.ID != repair.TaskID {
			continue
		}
		if !accepted[t.ID] && ready(t, accepted) {
			return i
		}
	}
	return -1
}
