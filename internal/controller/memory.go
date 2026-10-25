package controller

// Scoped memory as the controller sees it: how facts enter, what binds them to
// the content they came from, and what a task may be shown.
//
// There are two ways in. A builder's report becomes a claim, and only a claim:
// nothing the text says can make it more, because the kind and trust are set
// here, not read from it. A planner's decision is approved, and may supersede
// another entry of the same kind and scope by naming it. Neither writes SQL
// text: values and scopes are artifacts, the store keeps their hashes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// MaxMemoryValueBytes bounds one entry's value. Entries are shown whole or not
// at all, so a value that could never fit is refused when it is recorded.
const MaxMemoryValueBytes = 8 << 10

const maxScopeBytes = 16 << 10

// ClaimRequest is a fact a builder reported about its task.
type ClaimRequest struct {
	TaskID string
	Text   string
	// Scope is the path prefixes the claim is about.
	Scope []string
	// ReportHash optionally names the artifact the claim was taken from.
	ReportHash string
}

// DecisionRequest is something the planner established.
type DecisionRequest struct {
	TaskID string
	// Kind is approved_decision, interface_contract or unresolved_issue.
	Kind     string
	Text     string
	Scope    []string
	Required bool
	// Supersedes names the entry this replaces; it must be of the same kind and
	// scope.
	Supersedes string
	ReportHash string
}

// Selection is what a task may be shown and why.
type Selection struct {
	Entries     []memory.Entry
	Explanation memory.Explanation
}

// RecordClaim stores a builder's report text as a claim: kind builder_claim,
// claim trust, never required. Recording the same claim again returns the same
// id.
func (c *Controller) RecordClaim(ctx context.Context, runID string, req ClaimRequest) (string, error) {
	return c.record(ctx, runID, req.TaskID, memory.KindBuilderClaim, memory.TrustClaim, req.Text, req.Scope, false, "", req.ReportHash, "builder_report")
}

// RecordDecision stores a planner decision with approved trust.
func (c *Controller) RecordDecision(ctx context.Context, runID string, req DecisionRequest) (string, error) {
	switch req.Kind {
	case memory.KindApprovedDecision, memory.KindInterfaceContract, memory.KindUnresolvedIssue:
	default:
		return "", fmt.Errorf("%w: a planner decision is an approved_decision, interface_contract or unresolved_issue, not %q", workflow.ErrInvalidTransition, req.Kind)
	}
	return c.record(ctx, runID, req.TaskID, req.Kind, memory.TrustApproved, req.Text, req.Scope, req.Required, req.Supersedes, req.ReportHash, "planner_decision")
}

func (c *Controller) record(ctx context.Context, runID, taskID, kind, trust, text string, scope []string, required bool, supersedes, reportHash, origin string) (string, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: memory: %s", workflow.ErrInvalidTransition, fmt.Sprintf(format, a...))
	}
	switch {
	case strings.TrimSpace(text) == "":
		return "", bad("the text is empty")
	case len(text) > MaxMemoryValueBytes:
		return "", bad("the text is %d bytes; an entry holds at most %d", len(text), MaxMemoryValueBytes)
	case !utf8.ValidString(text):
		return "", bad("the text is not valid UTF-8")
	}
	norm, err := memory.NormalizeScope(scope)
	if err != nil {
		return "", err
	}
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	tasks, err := c.d.State.RunTasks(ctx, runID)
	if err != nil {
		return "", err
	}
	var task *state.TaskRow
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
		}
	}
	if task == nil {
		return "", fmt.Errorf("%w: run %s has no task %s", workflow.ErrConflict, runID, taskID)
	}
	deps := append([]string{}, task.DependsOn...)
	sort.Strings(deps)
	binding, err := c.binding(ctx, tasks, deps)
	if err != nil {
		return "", err
	}
	value, err := c.put(ctx, text)
	if err != nil {
		return "", err
	}
	scopeHash, err := c.putJSON(ctx, norm)
	if err != nil {
		return "", err
	}
	source, err := c.putJSON(ctx, struct {
		Origin     string `json:"origin"`
		RunID      string `json:"run_id"`
		TaskID     string `json:"task_id"`
		ReportHash string `json:"report_hash,omitempty"`
	}{origin, run.ID, taskID, reportHash})
	if err != nil {
		return "", err
	}
	e := memory.Entry{ID: derive(runID, "memory", origin, taskID, kind, value, scopeHash, supersedes), RunID: runID, TaskID: taskID, Kind: kind, Trust: trust,
		State: memory.StateActive, ScopeHash: scopeHash, SourceHash: source, ValidityHash: binding, ValueHash: value, SupersedesID: supersedes,
		Required: required, Dependencies: deps}
	if err := c.d.State.PutMemory(ctx, e); err != nil {
		return "", err
	}
	return e.ID, nil
}

// binding is the hash that ties an entry to the content it was derived from:
// for each task it depends on, that task's spec and the end snapshot of the
// attempt that was accepted. A fact about work that is not accepted yet cannot
// be bound, so it is not recorded; and when any of that content differs, the
// entry's hash is no longer current.
func (c *Controller) binding(ctx context.Context, tasks []state.TaskRow, deps []string) (string, error) {
	type part struct {
		Task     string `json:"task"`
		Spec     string `json:"spec_hash"`
		Snapshot string `json:"end_snapshot_hash"`
	}
	byID := map[string]state.TaskRow{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	parts := []part{}
	for _, d := range deps {
		t, ok := byID[d]
		if !ok {
			return "", fmt.Errorf("%w: no task %s to bind to", workflow.ErrConflict, d)
		}
		if t.AcceptedAttemptID == "" {
			return "", fmt.Errorf("%w: task %s is not accepted, so a fact derived from it cannot be bound to its content", workflow.ErrIncompleteEvidence, d)
		}
		att, err := c.d.State.GetAttempt(ctx, t.AcceptedAttemptID)
		if err != nil {
			return "", err
		}
		parts = append(parts, part{d, t.SpecHash, att.EndSnapshotHash})
	}
	return c.putJSON(ctx, parts)
}

// MemoryFor selects the entries task may be shown, with the reason for every
// inclusion and exclusion. An entry is current when the binding it was recorded
// with is still the binding of the content it depends on. Required entries that
// contradict each other return workflow.ErrMemoryConflict naming them, with the
// explanation alongside.
func (c *Controller) MemoryFor(ctx context.Context, runID, taskID string) (Selection, error) {
	tasks, err := c.d.State.RunTasks(ctx, runID)
	if err != nil {
		return Selection{}, err
	}
	var spec workflow.TaskSpec
	found := false
	for _, t := range tasks {
		if t.ID == taskID {
			raw, err := c.d.Artifacts.ReadAll(ctx, t.SpecHash, 1<<20)
			if err != nil {
				return Selection{}, err
			}
			if err := json.Unmarshal(raw, &spec); err != nil {
				return Selection{}, fmt.Errorf("%w: the spec of task %s is unreadable: %v", workflow.ErrCorruptArtifact, taskID, err)
			}
			found = true
		}
	}
	if !found {
		return Selection{}, fmt.Errorf("%w: run %s has no task %s", workflow.ErrConflict, runID, taskID)
	}
	entries, err := c.d.State.ListMemory(ctx, runID)
	if err != nil {
		return Selection{}, err
	}
	valid, err := c.currentBindings(ctx, tasks, entries)
	if err != nil {
		return Selection{}, err
	}
	for i := range entries {
		if err := c.hydrate(ctx, &entries[i]); err != nil {
			return Selection{}, err
		}
	}
	sel := Selection{Explanation: memory.Explain(entries, spec, valid)}
	sel.Entries, err = memory.Select(entries, spec, valid)
	return sel, err
}

// currentBindings is the set of binding hashes the entries would be recorded
// with now.
func (c *Controller) currentBindings(ctx context.Context, tasks []state.TaskRow, entries []memory.Entry) (map[string]bool, error) {
	valid := map[string]bool{}
	done := map[string]bool{}
	for _, e := range entries {
		key := strings.Join(e.Dependencies, "\x00")
		if done[key] {
			continue
		}
		done[key] = true
		h, err := c.binding(ctx, tasks, e.Dependencies)
		if errors.Is(err, workflow.ErrIncompleteEvidence) || errors.Is(err, workflow.ErrConflict) {
			continue // what it was bound to is gone or unfinished: nothing is current
		}
		if err != nil {
			return nil, err
		}
		valid[h] = true
	}
	return valid, nil
}

// InvalidateStaleMemory marks invalid every active entry whose binding no longer
// holds, and returns their ids. Nothing is deleted.
func (c *Controller) InvalidateStaleMemory(ctx context.Context, runID string) ([]string, error) {
	tasks, err := c.d.State.RunTasks(ctx, runID)
	if err != nil {
		return nil, err
	}
	entries, err := c.d.State.ListMemory(ctx, runID)
	if err != nil {
		return nil, err
	}
	valid, err := c.currentBindings(ctx, tasks, entries)
	if err != nil {
		return nil, err
	}
	return c.d.State.InvalidateMemory(ctx, runID, valid)
}

// hydrate loads an entry's bounded value and scope from their artifacts.
func (c *Controller) hydrate(ctx context.Context, e *memory.Entry) error {
	raw, err := c.d.Artifacts.ReadAll(ctx, e.ValueHash, MaxMemoryValueBytes)
	if err != nil {
		return fmt.Errorf("memory entry %s: its value: %w", e.ID, err)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("%w: the value of memory entry %s is not valid UTF-8", workflow.ErrCorruptArtifact, e.ID)
	}
	e.Value = string(raw)
	raw, err = c.d.Artifacts.ReadAll(ctx, e.ScopeHash, maxScopeBytes)
	if err != nil {
		return fmt.Errorf("memory entry %s: its scope: %w", e.ID, err)
	}
	if err := json.Unmarshal(raw, &e.Scope); err != nil {
		return fmt.Errorf("%w: the scope of memory entry %s is unreadable: %v", workflow.ErrCorruptArtifact, e.ID, err)
	}
	return nil
}
