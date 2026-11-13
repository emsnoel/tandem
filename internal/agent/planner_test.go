package agent

import "testing"

func TestOnlyClaudeAndCopilotCanPlan(t *testing.T) {
	for kind, want := range map[string]bool{"claude": true, "copilot": true, "codex": false, "": false, "opencode": false} {
		if got := CanPlan(kind); got != want {
			t.Errorf("CanPlan(%q) = %v, want %v", kind, got, want)
		}
	}
}
