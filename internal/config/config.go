// Package config merges tandem's configuration sources into one
// provenance-carrying result.
//
// Two orthogonal rules govern it. PRECEDENCE decides which source wins when
// several supply a value. TRUST CLASS decides which sources may supply a value
// at all: a repository may declare intent, but only the user may grant
// capability, so allow_widen, args and path are user-config only.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// RepoRole is the subset of a role block a repository may set.
type RepoRole struct {
	Kind         string
	Model        string
	Effort       string
	Autonomy     string
	AllowDegrade []string
}

type Repo struct {
	PlansDir        string
	CheckCommand    string
	ConventionsFile string
	PromptExtra     string
	BuilderKind     string // legacy; superseded by Builder.Kind
	Planner         *RepoRole
	Builder         *RepoRole
	Reviewer        *RepoRole
}

// CheckEngineSetting allows an old "durable" setting, so files that have one
// keep working, and refuses every other value by name.
func CheckEngineSetting(name, value string) error {
	if value == "" || value == "durable" {
		return nil
	}
	return fmt.Errorf("%s is set to %q, but the legacy engine is gone and durable is the only build engine; remove this setting", name, value)
}

// checkEngineValue applies CheckEngineSetting to .tandem.json's raw value. A
// value that isn't a string is refused too, by its JSON text.
func checkEngineValue(v json.RawMessage) error {
	if v == nil {
		return nil
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		s = string(v)
	}
	return CheckEngineSetting("build_engine", s)
}

// repoRoleAllowed lists keys a repository may set on a role block.
var repoRoleAllowed = map[string]bool{
	"kind": true, "model": true, "effort": true,
	"autonomy": true, "allow_degrade": true,
}

// repoRoleCapability lists keys that select authority or an executable. A
// repository is cloned from elsewhere, so these are user-config only.
var repoRoleCapability = []string{"allow_widen", "args", "path"}

// LoadRepo reads the per-repo manifest at path, which internal/paths resolves
// (it may still be the pre-rename .herdr.json). A missing file is not an error. Ignored keys
// produce warnings rather than failures, so one bad key never blocks a pairing.
func LoadRepo(path string) (*Repo, []string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Repo{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	str := func(k string) string {
		v, ok := raw[k]
		if !ok {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return ""
		}
		return s
	}

	r := &Repo{
		PlansDir:        str("plans_dir"),
		CheckCommand:    str("check_command"),
		ConventionsFile: str("conventions_file"),
		PromptExtra:     str("prompt_extra"),
		BuilderKind:     str("builder_kind"),
	}
	if err := checkEngineValue(raw["build_engine"]); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	var warns []string
	for _, name := range []string{"planner", "builder", "reviewer"} {
		blob, ok := raw[name]
		if !ok {
			continue
		}
		// A repository may not select the planner: no planner is launched, so a repo
		// value could only mis-select an agent on someone else's machine. The key is
		// dropped with a warning.
		if name == "planner" {
			warns = append(warns, fmt.Sprintf(
				"%s: the planner block is ignored -- the planner is your own environment, "+
					"not the project's. Use defaults.planner_kind in ~/.config/herdr/tandem.json.", path))
			continue
		}
		role, w, err := parseRepoRole(path, name, blob)
		if err != nil {
			return nil, nil, err
		}
		warns = append(warns, w...)
		if name == "reviewer" {
			r.Reviewer = role
		} else {
			r.Builder = role
		}
	}

	if r.BuilderKind != "" && r.Builder != nil && r.Builder.Kind != "" {
		warns = append(warns, fmt.Sprintf(
			"%s: both builder_kind=%q and builder.kind=%q are set; builder.kind wins",
			path, r.BuilderKind, r.Builder.Kind))
	}
	return r, warns, nil
}

func parseRepoRole(path, name string, blob json.RawMessage) (*RepoRole, []string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, nil, fmt.Errorf("%s: %s: %w", path, name, err)
	}

	var warns []string
	for _, k := range repoRoleCapability {
		if _, present := m[k]; present {
			warns = append(warns, fmt.Sprintf(
				"%s: %s.%s ignored — it selects capability, so it may only be set in ~/.config/herdr/tandem.json",
				path, name, k))
			delete(m, k)
		}
	}
	for k := range m {
		if !repoRoleAllowed[k] {
			warns = append(warns, fmt.Sprintf("%s: %s.%s is not a recognised key; ignored", path, name, k))
			delete(m, k)
		}
	}

	role := &RepoRole{}
	get := func(k string) string {
		v, ok := m[k]
		if !ok {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return ""
		}
		return s
	}
	role.Kind = get("kind")
	role.Model = get("model")
	role.Effort = get("effort")
	role.Autonomy = get("autonomy")
	if v, ok := m["allow_degrade"]; ok {
		_ = json.Unmarshal(v, &role.AllowDegrade)
	}
	return role, warns, nil
}
