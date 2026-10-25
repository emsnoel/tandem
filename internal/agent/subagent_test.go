package agent

import "testing"

// Which adapters can select a per-subagent capability. A claim here must be
// verified against the installed CLI; an overclaim would let the staffing
// advice recommend a floor nothing enforces.
//
// claude: agent definitions carry a default `model:`, which the dispatch-time
// model parameter overrides.
//
// codex: `spawn_agent` accepts `model` and `reasoning_effort` and honours both,
// as seen in children's turn_context in real rollout logs (children ran a
// different model and effort from their parent).
//
// copilot and opencode are unverified.
func TestSubagentEffortIsClaimedOnlyWhereVerified(t *testing.T) {
	for _, a := range []Adapter{claudeAdapter{}, codexAdapter{}} {
		if got := a.Supports("subagent_effort"); len(got) == 0 {
			t.Errorf("%s is verified to express a subagent effort axis, got %v", a.Kind(), got)
		}
	}
	for _, a := range []Adapter{copilotAdapter{}, opencodeAdapter{}} {
		if got := a.Supports("subagent_effort"); got != nil {
			t.Errorf("%s must not claim a subagent effort axis it cannot express, got %v", a.Kind(), got)
		}
	}
}

// The axis must use the shared effort vocabulary. It is checked as a prefix of
// EffortOrder: an adapter may stop short of `max` but never reorder or invent a
// rung.
func TestSubagentEffortUsesTheCanonicalVocabulary(t *testing.T) {
	for _, a := range []Adapter{claudeAdapter{}, codexAdapter{}} {
		got := a.Supports("subagent_effort")
		if len(got) > len(EffortOrder) {
			t.Fatalf("%s claims %d rungs, more than the ladder's %d", a.Kind(), len(got), len(EffortOrder))
		}
		for i, v := range got {
			if v != EffortOrder[i] {
				t.Fatalf("%s position %d: want %q, got %q", a.Kind(), i, EffortOrder[i], v)
			}
		}
	}
}

// Codex's spawn_agent `reasoning_effort` takes the same enum as the launch
// path's model_reasoning_effort, so the vocabularies must match, `max` omitted
// in both.
func TestCodexSubagentEffortMatchesItsOwnEffortAxis(t *testing.T) {
	c := codexAdapter{}
	launch, sub := c.Supports("effort"), c.Supports("subagent_effort")
	if len(launch) != len(sub) {
		t.Fatalf("effort %v and subagent_effort %v must agree", launch, sub)
	}
	for i := range launch {
		if launch[i] != sub[i] {
			t.Fatalf("position %d: effort %q but subagent_effort %q", i, launch[i], sub[i])
		}
	}
}
