package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

type manifestFact struct {
	ID       string `json:"id"`
	Reason   string `json:"reason"`
	Required bool   `json:"required"`
}

type persistedManifest struct {
	TaskID    string `json:"task_id"`
	Method    string `json:"estimate_method"`
	Limit     int    `json:"limit"`
	Estimated struct {
		Required int `json:"required"`
		Optional int `json:"optional"`
		Total    int `json:"total"`
	} `json:"estimated"`
	Sections   []taskcontext.Section `json:"sections"`
	Selected   []manifestFact        `json:"selected"`
	Omitted    []manifestFact        `json:"omitted"`
	PromptHash string                `json:"prompt_hash"`
	ConfigHash string                `json:"config_hash"`
}

func readManifest(ctx context.Context, art *artifact.Store, hash string) (persistedManifest, error) {
	raw, err := art.ReadAll(ctx, hash, 1<<20)
	if err != nil {
		return persistedManifest{}, err
	}
	var m persistedManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return persistedManifest{}, fmt.Errorf("%w: the context manifest %s is unreadable: %v", workflow.ErrCorruptArtifact, hash, err)
	}
	return m, nil
}

// ExplainedFact is one fact a packet showed or left out, and what is needed to
// fetch its text.
type ExplainedFact struct {
	ID        string `json:"id"`
	Reason    string `json:"reason"`
	Required  bool   `json:"required"`
	Kind      string `json:"kind"`
	Trust     string `json:"trust"`
	ValueHash string `json:"value_hash"`
}

// ContextExplanation is what an attempt's packet was built from, read back from
// its persisted manifest.
type ContextExplanation struct {
	AttemptID    string `json:"attempt_id"`
	TaskID       string `json:"task_id"`
	ManifestHash string `json:"manifest_hash"`
	Method       string `json:"estimate_method"`
	Limit        int    `json:"limit"`
	Estimated    struct {
		Required int `json:"required"`
		Optional int `json:"optional"`
		Total    int `json:"total"`
	} `json:"estimated"`
	Sections   []taskcontext.Section `json:"sections"`
	Selected   []ExplainedFact       `json:"selected"`
	Omitted    []ExplainedFact       `json:"omitted"`
	PromptHash string                `json:"prompt_hash"`
	ConfigHash string                `json:"config_hash"`
}

// ExplainContext shows what went into an attempt's packet. It reads the
// manifest persisted when the packet was built and never rebuilds it from the
// memory as it stands now, so it answers what the builder was actually given.
// Per-fact metadata that cannot change after a fact is recorded (kind, trust
// and the hash of its text) is looked up beside it; a fact's current state is
// deliberately not.
func ExplainContext(ctx context.Context, st *state.Store, art *artifact.Store, runID, attemptID string) (ContextExplanation, error) {
	owner, err := st.AttemptRun(ctx, attemptID)
	if err != nil || owner != runID {
		return ContextExplanation{}, fmt.Errorf("%w: run %s has no attempt %s", workflow.ErrConflict, runID, attemptID)
	}
	mh, found, err := st.AttemptManifest(ctx, attemptID)
	if err != nil {
		return ContextExplanation{}, err
	}
	if !found {
		return ContextExplanation{}, fmt.Errorf("%w: attempt %s has no context manifest", workflow.ErrIncompleteEvidence, attemptID)
	}
	m, err := readManifest(ctx, art, mh)
	if err != nil {
		return ContextExplanation{}, err
	}
	x := ContextExplanation{AttemptID: attemptID, TaskID: m.TaskID, ManifestHash: mh, Method: m.Method, Limit: m.Limit, Estimated: m.Estimated,
		Sections: m.Sections, PromptHash: m.PromptHash, ConfigHash: m.ConfigHash, Selected: []ExplainedFact{}, Omitted: []ExplainedFact{}}
	enrich := func(list []manifestFact) ([]ExplainedFact, error) {
		out := []ExplainedFact{}
		for _, f := range list {
			ef := ExplainedFact{ID: f.ID, Reason: f.Reason, Required: f.Required}
			if e, ok, err := st.GetMemory(ctx, f.ID); err != nil {
				return nil, err
			} else if ok {
				ef.Kind, ef.Trust, ef.ValueHash = e.Kind, e.Trust, e.ValueHash
			}
			out = append(out, ef)
		}
		return out, nil
	}
	if x.Selected, err = enrich(m.Selected); err != nil {
		return ContextExplanation{}, err
	}
	if x.Omitted, err = enrich(m.Omitted); err != nil {
		return ContextExplanation{}, err
	}
	return x, nil
}
