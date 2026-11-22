package main

import (
	"reflect"
	"slices"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
)

// mapEnv is a getenv over m.
func mapEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// A Codex planner reaches Herdr, writes the state home and the git directory,
// and asks before anything else its sandbox denies.
func TestCodexPlannerSpec(t *testing.T) {
	r := config.ResolveRole(agent.RolePlanner, &config.Repo{}, &config.User{}, mapEnv(nil), config.Overrides{Kind: "codex"})
	p := plannerPlaces{Socket: "/h/herdr.sock", StateHome: "/s/tandem/state", GitDir: "/repo/.git"}
	s := codexPlannerSpec(r, "/repo", p)
	if s.Kind != "codex" || s.Role != agent.RolePlanner || s.Autonomy != agent.Ask || s.Cwd != "/repo" {
		t.Fatalf("got %+v", s)
	}
	if s.HerdrSocket != p.Socket || !reflect.DeepEqual(s.Sandbox.WritableDirs, []string{p.StateHome, p.GitDir}) {
		t.Fatalf("got socket %q, dirs %q", s.HerdrSocket, s.Sandbox.WritableDirs)
	}
	a, _ := agent.Get("codex")
	l, err := a.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`permissions.tandem.network.unix_sockets={"/h/herdr.sock"="allow","/repo"="allow"}`,
		`permissions.tandem.filesystem={"/s/tandem/state"="write","/repo/.git"="write"}`,
		"on-request",
	} {
		if !slices.Contains(l.Args, want) {
			t.Errorf("missing %s in %q", want, l.Args)
		}
	}
	// A person sits at the planner, so it keeps Codex's update prompt.
	if slices.Contains(l.Args, "check_for_update_on_startup=false") {
		t.Errorf("a planner skipped the update check: %q", l.Args)
	}
}

// Outside a git checkout there is no git directory to grant, and an autonomy
// the person set is kept.
func TestCodexPlannerSpecKeepsTheUsersAutonomy(t *testing.T) {
	env := mapEnv(map[string]string{"HERDR_PLANNER_AUTONOMY": "auto"})
	r := config.ResolveRole(agent.RolePlanner, &config.Repo{}, &config.User{}, env, config.Overrides{Kind: "codex"})
	s := codexPlannerSpec(r, "/tmp/x", plannerPlaces{Socket: "/h.sock", StateHome: "/s"})
	if s.Autonomy != agent.Auto || !reflect.DeepEqual(s.Sandbox.WritableDirs, []string{"/s"}) {
		t.Fatalf("got autonomy %q, dirs %q", s.Autonomy, s.Sandbox.WritableDirs)
	}
}
