package controller

import (
	"context"
	"encoding/json"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
)

// Agents names the agents a run was started with. The reviewer is empty for a
// run without a reviewer pane.
type Agents struct {
	BuilderKind, BuilderSession, PlannerKind, PlannerSession string
	ReviewerKind, ReviewerSession                            string
}

// RunAgents reads the agents a run was started with, so a caller can build a
// transport for that run before building its controller.
func RunAgents(ctx context.Context, st *state.Store, art *artifact.Store, runID string) (Agents, error) {
	cfg, err := storedConfig(ctx, st, art, runID)
	if err != nil {
		return Agents{}, err
	}
	return Agents{BuilderKind: cfg.BuilderKind, BuilderSession: cfg.BuilderSession, PlannerKind: cfg.PlannerKind, PlannerSession: cfg.PlannerSession,
		ReviewerKind: cfg.ReviewerKind, ReviewerSession: cfg.ReviewerSession}, nil
}

// RunPlan reads the plan file path and collaboration id a run was started
// with. Either is empty when the run's configuration holds none, as an older
// run's has no plan path.
func RunPlan(ctx context.Context, st *state.Store, art *artifact.Store, runID string) (plan, collaboration string, err error) {
	cfg, err := storedConfig(ctx, st, art, runID)
	if err != nil {
		return "", "", err
	}
	return cfg.PlanPath, cfg.Collaboration, nil
}

func storedConfig(ctx context.Context, st *state.Store, art *artifact.Store, runID string) (runConfig, error) {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return runConfig{}, err
	}
	raw, err := art.ReadAll(ctx, run.ConfigHash, 1<<20)
	if err != nil {
		return runConfig{}, err
	}
	var cfg runConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return runConfig{}, err
	}
	return cfg, nil
}
