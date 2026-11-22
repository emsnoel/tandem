package agent

import "testing"

func TestPlannerKinds(t *testing.T) {
	for kind, want := range map[string]bool{"claude": true, "copilot": true, "agy": true, "codex": true, "": false, "opencode": false} {
		if got := CanPlan(kind); got != want {
			t.Errorf("CanPlan(%q) = %v, want %v", kind, got, want)
		}
	}
}
