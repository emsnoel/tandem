package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

func wantValue(t *testing.T, name string, got Value, value string, from Source) {
	t.Helper()
	if got.Value != value || got.From != from {
		t.Errorf("%s: got %+v, want {%q %s}", name, got, value, from)
	}
}

func mustResolve(t *testing.T, res ReviewerResolution) *Resolved {
	t.Helper()
	if res.Status != ReviewerResolved || res.Resolved == nil {
		t.Fatalf("got %+v, want a resolved reviewer", res)
	}
	return res.Resolved
}

func emptyUser() *User { return &User{Adapters: map[string]UserAdapter{}} }

func TestResolveReviewerNoneWithoutBlockOrFlag(t *testing.T) {
	res := ResolveReviewer(&Repo{}, emptyUser(),
		envFrom(map[string]string{"HERDR_REVIEWER_KIND": "codex"}), Overrides{})
	if res.Status != ReviewerNone || res.Resolved != nil {
		t.Fatalf("got %+v, want none", res)
	}
}

func TestResolveReviewerFromBlock(t *testing.T) {
	repo := &Repo{Reviewer: &RepoRole{Kind: "codex", Model: "gpt-6-astra"}}
	r := mustResolve(t, ResolveReviewer(repo, emptyUser(), envFrom(nil), Overrides{}))
	if r.Role != agent.RoleReviewer {
		t.Errorf("role = %q", r.Role)
	}
	wantValue(t, "kind", r.Kind, "codex", FromRepo)
	wantValue(t, "model", r.Model, "gpt-6-astra", FromRepo)
	wantValue(t, "autonomy", r.Autonomy, "auto", FromDefault)
}

// The reviewer must never inherit the builder's settings.
func TestResolveReviewerNeverReadsBuilder(t *testing.T) {
	repo := &Repo{
		Builder:  &RepoRole{Kind: "copilot", Model: "claude-sonnet-5.5", Effort: "high", Autonomy: "full"},
		Reviewer: &RepoRole{Kind: "codex"},
	}
	user := &User{Defaults: UserDefaults{BuilderKind: "opencode"}, Adapters: map[string]UserAdapter{}}
	env := envFrom(map[string]string{"HERDR_BUILDER_MODEL": "x"})
	r := mustResolve(t, ResolveReviewer(repo, user, env, Overrides{}))
	wantValue(t, "kind", r.Kind, "codex", FromRepo)
	wantValue(t, "model", r.Model, "", FromUnset)
	wantValue(t, "effort", r.Effort, "", FromUnset)
	wantValue(t, "autonomy", r.Autonomy, "auto", FromDefault)
}

func TestResolveReviewerIgnoresUserChoiceKeys(t *testing.T) {
	repo := &Repo{Reviewer: &RepoRole{Kind: "codex"}}
	user := &User{Adapters: map[string]UserAdapter{"codex": {
		Model: "cheap", Effort: "low", Autonomy: "full",
		Args: []string{"-c", "x"}, Path: "/bin/codex", AllowWiden: []string{"autonomy"},
		AllowDegrade: []string{"effort"},
	}}}
	r := mustResolve(t, ResolveReviewer(repo, user, envFrom(nil), Overrides{}))
	wantValue(t, "model", r.Model, "", FromUnset)
	wantValue(t, "effort", r.Effort, "", FromUnset)
	wantValue(t, "autonomy", r.Autonomy, "auto", FromDefault)
	if !reflect.DeepEqual(r.ExtraArgs, []string{"-c", "x"}) || r.Path != "/bin/codex" ||
		!reflect.DeepEqual(r.AllowWiden, []string{"autonomy"}) ||
		!reflect.DeepEqual(r.AllowDegrade, []string{"effort"}) {
		t.Errorf("machine keys not applied: %+v", r)
	}
}

func TestResolveReviewerPrecedence(t *testing.T) {
	repo := &Repo{Reviewer: &RepoRole{Kind: "codex", Model: "b-model", Effort: "low", Autonomy: "ask"}}
	env := envFrom(map[string]string{
		"HERDR_REVIEWER_KIND": "claude", "HERDR_REVIEWER_MODEL": "e-model",
		"HERDR_REVIEWER_EFFORT": "medium", "HERDR_REVIEWER_AUTONOMY": "readonly",
	})

	r := mustResolve(t, ResolveReviewer(repo, emptyUser(), env, Overrides{}))
	wantValue(t, "env kind", r.Kind, "claude", FromEnv)
	wantValue(t, "env model", r.Model, "e-model", FromEnv)
	wantValue(t, "env effort", r.Effort, "medium", FromEnv)
	wantValue(t, "env autonomy", r.Autonomy, "readonly", FromEnv)

	r = mustResolve(t, ResolveReviewer(repo, emptyUser(), env,
		Overrides{Kind: "opencode", Model: "f-model", Effort: "high", Autonomy: "full"}))
	wantValue(t, "flag kind", r.Kind, "opencode", FromFlag)
	wantValue(t, "flag model", r.Model, "f-model", FromFlag)
	wantValue(t, "flag effort", r.Effort, "high", FromFlag)
	wantValue(t, "flag autonomy", r.Autonomy, "full", FromFlag)

	r = mustResolve(t, ResolveReviewer(repo, emptyUser(), envFrom(nil), Overrides{}))
	wantValue(t, "block model", r.Model, "b-model", FromRepo)
	wantValue(t, "block effort", r.Effort, "low", FromRepo)
	wantValue(t, "block autonomy", r.Autonomy, "ask", FromRepo)
}

func TestResolveReviewerFlagTurnsItOn(t *testing.T) {
	r := mustResolve(t, ResolveReviewer(&Repo{}, emptyUser(), envFrom(nil), Overrides{Kind: "claude"}))
	wantValue(t, "kind", r.Kind, "claude", FromFlag)
}

func TestResolveReviewerBlockWithoutKindIsInvalid(t *testing.T) {
	repo := &Repo{Reviewer: &RepoRole{Model: "x"}}
	res := ResolveReviewer(repo, emptyUser(), envFrom(nil), Overrides{})
	if res.Status != ReviewerInvalid || !strings.Contains(res.Reason, "reviewer.kind") || res.Resolved != nil {
		t.Fatalf("got %+v, want invalid naming reviewer.kind", res)
	}
}
