package doctor

import (
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/herdr"
)

func TestThePlannerKindIsTheCallingPanes(t *testing.T) {
	p := healthy()
	p.agents = []herdr.Agent{{PaneID: "w1:p1", Kind: "copilot"}, {PaneID: "w1:p2", Kind: "codex"}}
	for pane, want := range map[string]string{"w1:p1": "copilot", "w1:p2": "", "w1:p9": "", "": ""} {
		if got := CallingPlannerKind(p, env(map[string]string{"HERDR_PANE_ID": pane})); got != want {
			t.Errorf("from pane %q: got %q, want %q", pane, got, want)
		}
	}
}

func TestDoctorChecksThePlannerItIsGiven(t *testing.T) {
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	root := t.TempDir()
	r, err := ResolveRoles(root, RoleFlags{Planner: config.Overrides{Kind: "copilot"}}, env(nil))
	if err != nil || r.Planner.Kind.Value != "copilot" {
		t.Fatalf("planner %+v, %v", r.Planner.Kind, err)
	}
	if r, _ = ResolveRoles(root, RoleFlags{}, env(nil)); r.Planner.Kind.Value != "claude" {
		t.Errorf("with no planner given: %+v", r.Planner.Kind)
	}
}
