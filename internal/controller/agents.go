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
	ShareContext                                             bool
}

// RunAgents reads the agents a run was started with, so a caller can build a
// transport for that run before building its controller.
func RunAgents(ctx context.Context, st *state.Store, art *artifact.Store, runID string) (Agents, error) {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return Agents{}, err
	}
	raw, err := art.ReadAll(ctx, run.ConfigHash, 1<<20)
	if err != nil {
		return Agents{}, err
	}
	var cfg runConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Agents{}, err
	}
	return Agents{BuilderKind: cfg.BuilderKind, BuilderSession: cfg.BuilderSession, PlannerKind: cfg.PlannerKind, PlannerSession: cfg.PlannerSession,
		ReviewerKind: cfg.ReviewerKind, ReviewerSession: cfg.ReviewerSession, ShareContext: cfg.ShareContext}, nil
}
