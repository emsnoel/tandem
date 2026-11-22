package agent

import "slices"

// PlannerKinds are the kinds that can plan. The planner is the agent a person
// runs tandem-pair from, and every tandem command it runs after that reaches
// Herdr's socket. Codex's own sandbox denies that socket, so a Codex planner
// starts through `tandem codex`, which adds the socket to Codex's permission
// profile (see codexSandbox). An agy planner must run in default mode, because
// Auto's sandbox blocks Herdr's socket.
var PlannerKinds = []string{"claude", "codex", "copilot", "agy"}

// CanPlan reports whether an agent of kind can be the planner.
func CanPlan(kind string) bool { return slices.Contains(PlannerKinds, kind) }
