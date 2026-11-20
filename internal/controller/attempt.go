package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

func (c *Controller) holdsLease(ctx context.Context, run state.Run) bool {
	owned, err := c.d.State.LeaseOwnedBy(ctx, run.CheckoutID, c.owner, c.fence(run.CheckoutID))
	return err == nil && owned
}

// observed are the facts only the controller establishes for an event.
func (c *Controller) observed(ctx context.Context, run state.Run, cfg runConfig) workflow.Observed {
	return workflow.Observed{
		WriterOwned:         c.holdsLease(ctx, run),
		SchedulingAllowed:   true,
		NoCheckPolicy:       cfg.CheckCommand == "",
		RequireVerification: cfg.AcceptancePolicy != "",
		Budget:              workflow.BudgetAvailable,
	}
}

// startAttempt creates an attempt for a task, its builder job and the
// attempt_created event whose effect delivers the task prompt. The prompt's
// nonce is derived from the attempt id, so running this again after a crash
// repairs it.
//
// The prompt is a packet built under the run's context limit. If it cannot be
// built (mandatory content too large, conflicting required memory, or a stale
// required fact), the run pauses with a code that says which and nothing is
// created.
func (c *Controller) startAttempt(ctx context.Context, run state.Run, cfg runConfig, task state.TaskRow, ordinal int, parentID, startSnapshot string, rc repairContext) error {
	attemptID, err := c.d.NewID()
	if err != nil {
		return err
	}
	spec, err := c.prepareAttempt(ctx, run, cfg, task, attemptID, ordinal, parentID, startSnapshot, rc)
	if err != nil {
		return err
	}
	if err := c.d.State.CreateAttempt(ctx, spec); err != nil {
		return err
	}
	return c.afterAttemptCreated(ctx, run, cfg, attemptID)
}

// prepareAttempt builds an attempt's packet and everything its row needs,
// without creating it. A packet that cannot be built pauses the run for the
// reason, so nothing is spent on an attempt that could not be sent.
func (c *Controller) prepareAttempt(ctx context.Context, run state.Run, cfg runConfig, task state.TaskRow, attemptID string, ordinal int, parentID, startSnapshot string, rc repairContext) (state.AttemptSpec, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return state.AttemptSpec{}, err
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, task.SpecHash, 4<<20)
	if err != nil {
		return state.AttemptSpec{}, err
	}
	var spec workflow.TaskSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return state.AttemptSpec{}, fmt.Errorf("%w: the spec of task %s is unreadable: %v", workflow.ErrCorruptArtifact, task.ID, err)
	}
	entries, err := c.contextEntries(ctx, run.ID, tasks)
	if err != nil {
		return state.AttemptSpec{}, err
	}
	pkt, err := taskcontext.Build(packetRequest(cfg, run.ID, run.PlanHash, startSnapshot, attemptID, task.Ordinal, len(tasks), spec, entries, rc.Note))
	if err != nil {
		code := ""
		switch {
		case errors.Is(err, workflow.ErrContextTooLarge):
			code = pauseContext
		case errors.Is(err, workflow.ErrMemoryConflict):
			code = pauseMemory
		case errors.Is(err, workflow.ErrIncompleteEvidence):
			code = pauseIncomplete
		}
		if code != "" {
			return state.AttemptSpec{}, errors.Join(fmt.Errorf("task %s was not started: %w", task.ID, err), c.d.State.SetRunState(ctx, run.ID, "paused", code))
		}
		return state.AttemptSpec{}, err
	}
	packet, err := c.put(ctx, pkt.Prompt)
	if err != nil {
		return state.AttemptSpec{}, err
	}
	manifest, err := c.put(ctx, string(pkt.Manifest)+"\n")
	if err != nil {
		return state.AttemptSpec{}, err
	}
	return state.AttemptSpec{ID: attemptID, RunID: run.ID, TaskID: task.ID, Ordinal: ordinal,
		ParentID: parentID, StartSnapshotHash: startSnapshot, PacketHash: packet, ManifestHash: manifest, Refs: rc.Refs}, nil
}

// afterAttemptCreated starts a created attempt: its builder job and the
// attempt_created event whose effect delivers the prompt.
func (c *Controller) afterAttemptCreated(ctx context.Context, run state.Run, cfg runConfig, attemptID string) error {
	if c.hooks.afterAttemptCreated != nil {
		if err := c.hooks.afterAttemptCreated(); err != nil {
			return err
		}
	}
	att, err := c.d.State.GetAttempt(ctx, attemptID)
	if err != nil {
		return err
	}
	return c.ensureStarted(ctx, run, cfg, att)
}

// contextEntries loads the run's memory with each entry's text, marking as
// invalid any whose binding no longer holds, so a packet reports a stale
// required fact instead of rendering without it.
func (c *Controller) contextEntries(ctx context.Context, runID string, tasks []state.TaskRow) ([]memory.Entry, error) {
	entries, err := c.d.State.ListMemory(ctx, runID)
	if err != nil {
		return nil, err
	}
	valid, err := c.currentBindings(ctx, tasks, entries)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if err := c.hydrate(ctx, &entries[i]); err != nil {
			return nil, err
		}
		if entries[i].State == memory.StateActive && !valid[entries[i].ValidityHash] {
			entries[i].State = memory.StateInvalid
		}
	}
	return entries, nil
}

// ensureStarted makes sure a pending attempt has its builder job and its
// attempt_created event. Both are keyed by the attempt id, so repeating it
// after a crash adds nothing.
func (c *Controller) ensureStarted(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) error {
	if att.Version != 0 {
		return nil
	}
	jobID := derive(att.ID, "builder-job")
	if _, found, err := c.d.State.GetJob(ctx, jobID); err != nil {
		return err
	} else if !found {
		if err := c.d.State.InsertJob(ctx, workflow.Job{ID: jobID, AttemptID: att.ID, Kind: "builder", State: "running",
			InputHash: att.PacketHash, ConfigHash: run.ConfigHash, RunnerKind: "builder-transport"}); err != nil {
			return err
		}
	}
	_, err := c.d.State.ApplyWith(ctx, workflow.Event{ID: derive(att.ID, "created"), RunID: run.ID, AttemptID: att.ID,
		Kind: workflow.EventAttemptCreated, PayloadHash: att.PacketHash, ExpectedVersion: 0}, c.observed(ctx, run, cfg))
	return err
}

// captureEvidence snapshots the tree now, diffs it against the attempt's start,
// and records both on the attempt. It fails, transiently, when the tree cannot
// be captured exactly: a submission is never applied against a guess.
func (c *Controller) captureEvidence(ctx context.Context, cfg runConfig, att workflow.Attempt) (patchText string, err error) {
	start, err := snapshot.LoadManifest(ctx, c.d.Artifacts, att.StartSnapshotHash)
	if err != nil {
		return "", err
	}
	end, err := snapshot.Capture(ctx, cfg.Root, c.d.Artifacts)
	if err != nil {
		return "", err
	}
	if !end.Complete {
		return "", fmt.Errorf("%w: the working tree cannot be captured exactly yet: %s", workflow.ErrBusy, strings.Join(end.Errors, "; "))
	}
	patch, err := snapshot.Diff(start, end)
	if err != nil {
		return "", err
	}
	patchHash, err := snapshot.StorePatch(ctx, c.d.Artifacts, patch)
	if err != nil {
		return "", err
	}
	textHash, err := snapshot.RenderPatch(ctx, c.d.Artifacts, patch)
	if err != nil {
		return "", err
	}
	for _, h := range []string{end.Hash, patchHash, textHash} {
		if err := c.d.State.AdoptArtifact(ctx, h); err != nil {
			return "", err
		}
	}
	if err := c.d.State.RecordAttemptEvidence(ctx, att.ID, end.Hash, patchHash); err != nil {
		return "", err
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, textHash, 4<<20)
	return string(raw), err
}
