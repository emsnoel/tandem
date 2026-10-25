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

// packetRequest is the context request for one attempt of a task: the task,
// the memory it may be shown, and the controller's header and footer, which
// name the attempt's identity.
func packetRequest(cfg runConfig, runID, planHash, startSnapshot, attemptID string, ordinal, total int, spec workflow.TaskSpec, entries []memory.Entry, note string) taskcontext.Request {
	eventID := derive(attemptID, "created")
	p := promptData{Task: ordinal, Total: total, Slug: spec.ID, Plan: cfg.PlanPath, Root: cfg.Root, Note: note,
		Run: runID, Attempt: attemptID, Job: derive(attemptID, "builder-job"),
		Effect: state.EffectID(eventID, 0), Nonce: state.EffectNonce(eventID, 0), CheckCommand: cfg.CheckCommand}
	return taskcontext.Request{RunID: runID, AttemptID: attemptID, PlanHash: planHash, StartSnapshotHash: startSnapshot,
		Task: spec, Entries: entries, MaxEstimatedTokens: cfg.ContextLimit, Header: promptHeader(p), Footer: promptFooter(p)}
}

type promptData struct {
	Task, Total                      int
	Slug, Plan, Root, Note           string
	Run, Attempt, Job, Effect, Nonce string
	CheckCommand                     string
}

// promptHeader says which task this is. It is part of the packet's mandatory
// text.
func promptHeader(p promptData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Implement task %d of %d (%s) of the plan at %s. Working directory: %s.", p.Task, p.Total, p.Slug, p.Plan, p.Root)
	if p.Note != "" {
		fmt.Fprintf(&b, "\n\n%s", p.Note)
	}
	return b.String()
}

// promptFooter names the identity the builder echoes back, and says to
// acknowledge before touching anything.
func promptFooter(p promptData) string {
	var b strings.Builder
	id := fmt.Sprintf("--task %d --run %s --attempt %s --job %s --effect %s --nonce %s", p.Task, p.Run, p.Attempt, p.Job, p.Effect, p.Nonce)
	fmt.Fprintf(&b, "BEFORE YOU EDIT ANYTHING, acknowledge this prompt. It is your first action:\n\n    tandem-handback %s --started\n\n", id)
	b.WriteString("Do the task. Do not commit unless the brief says to.")
	if p.CheckCommand != "" {
		b.WriteString(" Tandem runs the project's check itself after you hand back; do not paste its output.")
	}
	fmt.Fprintf(&b, "\n\nWhen you finish, write a report (under 20 lines: whether the task is complete, anything you changed that the brief did not ask for, anything you could not verify) to a file and hand it back:\n\n    tandem-handback %s --report <file>\n\nAdd --stuck if you could not finish. That call returns immediately. Then stop.", id)
	return b.String()
}

type reviewPromptData struct {
	Task                          int
	Slug                          string
	Job, Attempt, PatchHash, Tree string
	Patch                         string
	WarmPane                      bool
}

// reviewPrompt asks for a structured review. The result names the job, the
// attempt and the exact patch and tree it examined; a result that names others
// is refused.
func reviewPrompt(p reviewPromptData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review the work on task %d (%s). Look hard for what the brief did not ask for as well as what it did.\n\n", p.Task, p.Slug)
	if p.WarmPane {
		fmt.Fprintf(&b, "%s\n\n", judgeAlone)
	}
	fmt.Fprintf(&b, "Reply with ONE JSON object and nothing else:\n\n")
	fmt.Fprintf(&b, "{\"schema_version\":1,\"job_id\":%q,\"attempt_id\":%q,\"patch_hash\":%q,\"tree_hash\":%q,\n \"status\":\"passed|changes_requested|failed\",\n \"findings\":[{\"severity\":\"high|medium|low\",\"path\":\"file\",\"disposition\":\"open\",\"message\":\"what is wrong\",\"start_line\":null,\"end_line\":null}]}\n\n",
		p.Job, p.Attempt, p.PatchHash, p.Tree)
	b.WriteString("changes_requested must list at least one finding. Do not edit files.\n\nThe patch:\n\n")
	b.WriteString(p.Patch)
	return b.String()
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
