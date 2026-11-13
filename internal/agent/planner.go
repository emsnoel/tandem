package agent

import "slices"

// PlannerKinds are the kinds that can plan. The planner is the agent a person
// runs tandem-pair from, and every tandem command it runs after that reaches
// Herdr's socket. Codex's sandbox denies that socket unless Codex runs with no
// sandbox at all, so Codex builds and reviews but does not plan.
var PlannerKinds = []string{"claude", "copilot"}

// CanPlan reports whether an agent of kind can be the planner.
func CanPlan(kind string) bool { return slices.Contains(PlannerKinds, kind) }
