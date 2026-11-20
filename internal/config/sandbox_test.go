package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

// sandbox is a user-only machine key, like args. It reaches the builder and
// the reviewer of its kind, with "~/" expanded and relative entries dropped.
func TestSandboxComesFromTheUsersAdapterEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tandem.json")
	body := `{"adapters":{"codex":{"sandbox":{"writable_dirs":["~/.gradle","/opt/cache","rel"],"network":true}}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	user, err := LoadUser(path)
	if err != nil {
		t.Fatal(err)
	}
	env := envFrom(map[string]string{"HOME": "/home/u"})
	want := agent.SandboxGrant{WritableDirs: []string{"/home/u/.gradle", "/opt/cache"}, Network: true}

	b := ResolveRole(agent.RoleBuilder, &Repo{Builder: &RepoRole{Kind: "codex"}}, user, env, Overrides{})
	if !reflect.DeepEqual(b.Spec("/repo").Sandbox, want) {
		t.Fatalf("builder: got %+v, want %+v", b.Spec("/repo").Sandbox, want)
	}
	rv := mustResolve(t, ResolveReviewer(&Repo{Reviewer: &RepoRole{Kind: "codex"}}, user, env, Overrides{}))
	if !reflect.DeepEqual(rv.Spec("/repo").Sandbox, want) {
		t.Fatalf("reviewer: got %+v, want %+v", rv.Spec("/repo").Sandbox, want)
	}
	// The planner is never launched, and another kind's entry does not leak across.
	p := ResolveRole(agent.RolePlanner, &Repo{}, &User{Adapters: user.Adapters, Defaults: UserDefaults{PlannerKind: "codex"}}, env, Overrides{})
	c := ResolveRole(agent.RoleBuilder, &Repo{Builder: &RepoRole{Kind: "claude"}}, user, env, Overrides{})
	for name, g := range map[string]agent.SandboxGrant{"planner": p.Sandbox, "claude builder": c.Sandbox} {
		if !reflect.DeepEqual(g, agent.SandboxGrant{}) {
			t.Errorf("%s: got %+v, want no grant", name, g)
		}
	}
}

// A repository cannot widen the sandbox: the key grants authority.
func TestRepoSandboxIsIgnoredWithAWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".tandem.json")
	body := `{"reviewer":{"kind":"codex","sandbox":{"writable_dirs":["/"],"network":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, warns, err := LoadRepo(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "reviewer.sandbox ignored") {
		t.Fatalf("warnings: %q", warns)
	}
	rv := mustResolve(t, ResolveReviewer(repo, &User{Adapters: map[string]UserAdapter{}}, envFrom(nil), Overrides{}))
	if !reflect.DeepEqual(rv.Sandbox, agent.SandboxGrant{}) {
		t.Fatalf("got %+v, want no grant", rv.Sandbox)
	}
}
