package trust

import (
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

func TestRegisterNoGatesRecordsReason(t *testing.T) {
	const kind = "test-kind-no-gates"
	if _, ok := noGates[kind]; ok {
		t.Fatalf("test kind %q already registered; pick a name unique to this test", kind)
	}
	RegisterNoGates(kind, "no trust dialog exists for this test kind")

	reason, ok := noGates[kind]
	if !ok {
		t.Fatalf("RegisterNoGates(%q, ...) did not record an entry", kind)
	}
	if reason != "no trust dialog exists for this test kind" {
		t.Fatalf("got reason %q, want %q", reason, "no trust dialog exists for this test kind")
	}
	if _, inGateRegistry := registry[kind]; inGateRegistry {
		t.Fatalf("RegisterNoGates(%q, ...) must not add an entry to the real gate registry", kind)
	}
}

// Every kind internal/agent can launch must be either in registry (it has
// gates) or in noGates (it has none on purpose). A kind in neither would
// silently get no prelaunch checks.
func TestEveryAgentKindIsAccountedFor(t *testing.T) {
	for _, kind := range agent.Kinds() {
		_, hasGates := registry[kind]
		_, hasNoGatesReason := noGates[kind]
		switch {
		case !hasGates && !hasNoGatesReason:
			t.Errorf("agent kind %q has no registered trust gate and no RegisterNoGates() declaration -- "+
				"either register a Gate for it, or add a file in this package calling "+
				"trust.RegisterNoGates(%q, \"...\") to declare that on purpose", kind, kind)
		case hasGates && hasNoGatesReason:
			t.Errorf("agent kind %q is registered both with real gates and RegisterNoGates() -- remove one", kind)
		}
	}
}

// A gate that needs a human stops only a role that runs unattended. Anything
// short of Manual never stops a launch.
func TestBlocksManualOnlyForUnattendedRoles(t *testing.T) {
	cases := []struct {
		role   agent.Role
		status agent.Status
		want   bool
	}{
		{agent.RoleBuilder, agent.StatusManual, true},
		{agent.RoleReviewer, agent.StatusManual, true},
		{agent.RolePlanner, agent.StatusManual, false},
		{agent.RoleBuilder, agent.StatusWarning, false},
		{agent.RoleBuilder, agent.StatusCurrent, false},
	}
	for _, c := range cases {
		if got := Blocks(c.role, agent.Action{Status: c.status}); got != c.want {
			t.Errorf("Blocks(%s, %s) = %v, want %v", c.role, c.status, got, c.want)
		}
	}
}
