package config

import (
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPrecedenceOrder(t *testing.T) {
	repo := &Repo{Builder: &RepoRole{Kind: "codex", Model: "repo-model"}}
	user := &User{Adapters: map[string]UserAdapter{"codex": {Model: "user-model"}}}

	// user only
	r := ResolveRole(agent.RoleBuilder, &Repo{}, user, envFrom(nil), Overrides{Kind: "codex"})
	if r.Model.Value != "user-model" || r.Model.From != FromUser {
		t.Errorf("user tier: got %+v", r.Model)
	}
	// repo beats user
	r = ResolveRole(agent.RoleBuilder, repo, user, envFrom(nil), Overrides{})
	if r.Model.Value != "repo-model" || r.Model.From != FromRepo {
		t.Errorf("repo tier: got %+v", r.Model)
	}
	// env beats repo
	r = ResolveRole(agent.RoleBuilder, repo, user,
		envFrom(map[string]string{"HERDR_BUILDER_MODEL": "env-model"}), Overrides{})
	if r.Model.Value != "env-model" || r.Model.From != FromEnv {
		t.Errorf("env tier: got %+v", r.Model)
	}
	// flag beats env
	r = ResolveRole(agent.RoleBuilder, repo, user,
		envFrom(map[string]string{"HERDR_BUILDER_MODEL": "env-model"}),
		Overrides{Model: "flag-model"})
	if r.Model.Value != "flag-model" || r.Model.From != FromFlag {
		t.Errorf("flag tier: got %+v", r.Model)
	}
}

func TestDefaultsPreserveTodaysBehaviour(t *testing.T) {
	r := ResolveRole(agent.RoleBuilder, &Repo{}, &User{Adapters: map[string]UserAdapter{}},
		envFrom(nil), Overrides{})
	if r.Kind.Value != "codex" || r.Kind.From != FromDefault {
		t.Errorf("builder kind: got %+v", r.Kind)
	}
	if r.Autonomy.Value != "auto" {
		t.Errorf("builder autonomy default must match today's workspace-write + approvals-off; got %+v", r.Autonomy)
	}
	p := ResolveRole(agent.RolePlanner, &Repo{}, &User{Adapters: map[string]UserAdapter{}},
		envFrom(nil), Overrides{})
	if p.Kind.Value != "claude" {
		t.Errorf("planner kind: got %+v", p.Kind)
	}
}

func TestLegacyBuilderKindStillResolves(t *testing.T) {
	r := ResolveRole(agent.RoleBuilder, &Repo{BuilderKind: "opencode"},
		&User{Adapters: map[string]UserAdapter{}}, envFrom(nil), Overrides{})
	if r.Kind.Value != "opencode" || r.Kind.From != FromRepo {
		t.Fatalf("got %+v", r.Kind)
	}
}

func TestLegacySandboxEnvTranslatesWithWarning(t *testing.T) {
	r := ResolveRole(agent.RoleBuilder, &Repo{}, &User{Adapters: map[string]UserAdapter{}},
		envFrom(map[string]string{"HERDR_BUILDER_SANDBOX": "danger-full-access"}), Overrides{})
	if r.Autonomy.Value != "full" {
		t.Errorf("got %+v", r.Autonomy)
	}
	if len(r.Warnings) == 0 {
		t.Error("the legacy variables must warn about their replacement")
	}
}

// SECURITY: allow_widen may only ever come from user config.
func TestAllowWidenComesOnlyFromUserConfig(t *testing.T) {
	user := &User{Adapters: map[string]UserAdapter{"opencode": {AllowWiden: []string{"autonomy"}}}}
	r := ResolveRole(agent.RoleBuilder, &Repo{Builder: &RepoRole{Kind: "opencode"}},
		user, envFrom(nil), Overrides{})
	if len(r.AllowWiden) != 1 || r.AllowWiden[0] != "autonomy" {
		t.Fatalf("got %v", r.AllowWiden)
	}
}

func TestSpecConversion(t *testing.T) {
	r := ResolveRole(agent.RoleBuilder, &Repo{Builder: &RepoRole{Kind: "opencode", Autonomy: "full"}},
		&User{Adapters: map[string]UserAdapter{}}, envFrom(nil), Overrides{})
	s := r.Spec("/repo")
	if s.Kind != "opencode" || s.Autonomy != agent.Full || s.Cwd != "/repo" || s.Role != agent.RoleBuilder {
		t.Fatalf("got %+v", s)
	}
}

func TestMarkAdoptedRewritesProvenance(t *testing.T) {
	r := ResolveRole(agent.RoleBuilder,
		&Repo{Builder: &RepoRole{Kind: "codex", Model: "gpt-5.4", Autonomy: "auto"}},
		&User{Adapters: map[string]UserAdapter{}}, envFrom(nil), Overrides{})

	got := r.MarkAdopted("opencode")

	if got.Kind.Value != "opencode" || got.Kind.From != FromAdopted {
		t.Fatalf("kind must become the RUNNING agent's, marked adopted; got %+v", got.Kind)
	}
	for _, v := range []Value{got.Model, got.Autonomy} {
		if v.Value == "" {
			continue
		}
		if v.From != FromAdopted {
			t.Errorf("configured axes are unenforced on an adopted agent and must say so; got %+v", v)
		}
	}
	if len(got.Unenforced) == 0 {
		t.Error("Unenforced must name the axes that were configured but not applied")
	}
}
