package config

import (
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
)

type Source string

const (
	FromFlag    Source = "flag"
	FromEnv     Source = "env"
	FromRepo    Source = "repo"
	FromUser    Source = "user"
	FromDefault Source = "default"
	FromUnset   Source = "unset"
	// FromAdopted marks a value that describes a RUNNING agent rather than one
	// tandem chose. It is not a precedence tier: MarkAdopted writes it
	// after resolution so configured values are not reported as in effect.
	FromAdopted Source = "adopted"
)

// Value is a resolved setting together with where it came from. Provenance is
// not decoration: without it, a precedence chain can only be debugged by
// bisecting the sources by hand.
type Value struct {
	Value string `json:"value"`
	From  Source `json:"from"`
}

// Overrides carries command-line flags, the highest-precedence source.
type Overrides struct {
	Kind     string
	Model    string
	Effort   string
	Autonomy string
}

type Resolved struct {
	Role         agent.Role `json:"role"`
	Kind         Value      `json:"kind"`
	Model        Value      `json:"model"`
	Effort       Value      `json:"effort"`
	Autonomy     Value      `json:"autonomy"`
	AllowDegrade []string   `json:"allow_degrade"`
	AllowWiden   []string   `json:"allow_widen"`
	ExtraArgs    []string   `json:"args"`
	Path         string     `json:"path"`
	Warnings     []string   `json:"warnings"`
	// Unenforced names axes that were configured but cannot apply, because the
	// agent was adopted rather than launched.
	Unenforced []string `json:"unenforced,omitempty"`
}

// MarkAdopted rewrites a resolution to describe an agent that was adopted
// rather than launched. Its existing model, effort, and autonomy cannot be
// changed without destroying the context adoption exists to preserve.
func (r Resolved) MarkAdopted(runningKind string) Resolved {
	r.Kind = Value{Value: runningKind, From: FromAdopted}
	for _, f := range []struct {
		name string
		v    *Value
	}{{"model", &r.Model}, {"effort", &r.Effort}, {"autonomy", &r.Autonomy}} {
		if f.v.Value == "" {
			continue
		}
		r.Unenforced = append(r.Unenforced, f.name+"="+f.v.Value)
		f.v.From = FromAdopted
	}
	return r
}

type candidate struct {
	v    string
	from Source
}

func pick(cands ...candidate) Value {
	for _, c := range cands {
		if c.v != "" {
			return Value{Value: c.v, From: c.from}
		}
	}
	return Value{From: FromUnset}
}

// ResolveRole merges every source for one role. getenv is injected so tests
// never depend on the ambient environment.
func ResolveRole(role agent.Role, repo *Repo, user *User, getenv func(string) string, flags Overrides) Resolved {
	prefix := "HERDR_" + strings.ToUpper(string(role)) + "_"

	rr := repo.Planner
	if role == agent.RoleBuilder {
		rr = repo.Builder
	}
	if rr == nil {
		rr = &RepoRole{}
	}

	// The legacy top-level builder_kind stands in for builder.kind when the
	// role block does not set one.
	repoKind := rr.Kind
	if repoKind == "" && role == agent.RoleBuilder {
		repoKind = repo.BuilderKind
	}

	userKind := user.Defaults.PlannerKind
	defKind, defAutonomy := "claude", ""
	if role == agent.RoleBuilder {
		userKind = user.Defaults.BuilderKind
		// The unattended builder must not stall at approval prompts, but its
		// sandbox is deliberately kept. Do not "fix" a sandbox denial by
		// widening this default to full: the mailbox exists precisely because a
		// builder that can reach the Herdr socket is a bad trade.
		defKind, defAutonomy = "codex", "auto"
	}

	r := Resolved{Role: role}
	r.Kind = pick(
		candidate{flags.Kind, FromFlag},
		candidate{getenv(prefix + "KIND"), FromEnv},
		candidate{repoKind, FromRepo},
		candidate{userKind, FromUser},
		candidate{defKind, FromDefault},
	)

	ua := user.Adapters[r.Kind.Value]

	r.Model = pick(
		candidate{flags.Model, FromFlag},
		candidate{getenv(prefix + "MODEL"), FromEnv},
		candidate{rr.Model, FromRepo},
		candidate{ua.Model, FromUser},
	)
	r.Effort = pick(
		candidate{flags.Effort, FromFlag},
		candidate{getenv(prefix + "EFFORT"), FromEnv},
		candidate{rr.Effort, FromRepo},
		candidate{ua.Effort, FromUser},
	)
	r.Autonomy = pick(
		candidate{flags.Autonomy, FromFlag},
		candidate{getenv(prefix + "AUTONOMY"), FromEnv},
		candidate{rr.Autonomy, FromRepo},
		candidate{ua.Autonomy, FromUser},
		candidate{defAutonomy, FromDefault},
	)

	// allow_degrade may come from either tier; allow_widen, args and path are
	// user-only, which LoadRepo has already enforced by dropping them.
	r.AllowDegrade = union(rr.AllowDegrade, ua.AllowDegrade)
	r.AllowWiden = ua.AllowWiden
	r.ExtraArgs = ua.Args
	r.Path = ua.Path

	if role == agent.RoleBuilder && r.Kind.Value == "codex" {
		if a, w := legacySandboxEnv(getenv); w != "" {
			r.Warnings = append(r.Warnings, w)
			if a != "" {
				r.Autonomy = Value{Value: a, From: FromEnv}
			}
		}
	}
	return r
}

// legacySandboxEnv translates the pre-vocabulary HERDR_BUILDER_SANDBOX and
// HERDR_BUILDER_APPROVAL into an autonomy value. Both are documented in the
// README, so they keep working; they are Codex vocabulary, so they apply only
// to a Codex builder.
func legacySandboxEnv(getenv func(string) string) (autonomy, warning string) {
	sandbox, approval := getenv("HERDR_BUILDER_SANDBOX"), getenv("HERDR_BUILDER_APPROVAL")
	if sandbox == "" && approval == "" {
		return "", ""
	}
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	if approval == "" {
		approval = "never"
	}
	switch {
	case sandbox == "danger-full-access":
		autonomy = "full"
	case sandbox == "read-only":
		autonomy = "readonly"
	case sandbox == "workspace-write" && approval == "never":
		autonomy = "auto"
	case sandbox == "workspace-write" && approval == "on-request":
		autonomy = "ask"
	}
	if autonomy == "" {
		return "", "HERDR_BUILDER_SANDBOX/HERDR_BUILDER_APPROVAL name a combination with no normalised equivalent; ignored. Use HERDR_BUILDER_AUTONOMY."
	}
	return autonomy, fmt.Sprintf(
		"HERDR_BUILDER_SANDBOX/HERDR_BUILDER_APPROVAL are deprecated; translated to autonomy=%s. Use HERDR_BUILDER_AUTONOMY.", autonomy)
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Spec converts the resolved configuration into the adapter's input.
func (r Resolved) Spec(cwd string) agent.Spec {
	return agent.Spec{
		Kind:         r.Kind.Value,
		Role:         r.Role,
		Model:        r.Model.Value,
		Effort:       agent.Effort(r.Effort.Value),
		Autonomy:     agent.Autonomy(r.Autonomy.Value),
		Cwd:          cwd,
		ExtraArgs:    r.ExtraArgs,
		AllowDegrade: r.AllowDegrade,
		AllowWiden:   r.AllowWiden,
	}
}
