package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRepoRoleBlocks(t *testing.T) {
	p := writeRepo(t, `{
	  "plans_dir": "docs/plans",
	  "builder": {"kind":"opencode","model":"anthropic/claude-sonnet-5",
	              "autonomy":"full","allow_degrade":["effort"]}
	}`)
	r, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if r.Builder.Kind != "opencode" || r.Builder.Autonomy != "full" {
		t.Fatalf("got %+v", r.Builder)
	}
	if len(r.Builder.AllowDegrade) != 1 || r.Builder.AllowDegrade[0] != "effort" {
		t.Fatalf("got allow_degrade %v", r.Builder.AllowDegrade)
	}
}

func TestLoadRepoMissingFileIsNotAnError(t *testing.T) {
	r, _, err := LoadRepo(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || r == nil {
		t.Fatalf("got (%v, %v), want an empty Repo and no error", r, err)
	}
}

// SECURITY: capability-bearing keys must be ignored in repo config.
func TestLoadRepoIgnoresCapabilityKeys(t *testing.T) {
	for _, key := range []string{"allow_widen", "args", "path"} {
		p := writeRepo(t, `{"builder":{"kind":"codex","`+key+`":`+sample(key)+`}}`)
		r, warns, err := LoadRepo(p)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if len(warns) == 0 || !strings.Contains(strings.Join(warns, "\n"), key) {
			t.Errorf("%s: expected a warning naming the key; got %v", key, warns)
		}
		if r.Builder.Kind != "codex" {
			t.Errorf("%s: the rest of the block must still load", key)
		}
	}
}

// A repository may not select the planner. Nothing launches one, so a repo
// value could only mis-select an agent on someone else's machine.
func TestLoadRepoIgnoresThePlannerBlock(t *testing.T) {
	p := writeRepo(t, `{"planner":{"kind":"opencode"},"builder":{"kind":"codex"}}`)
	r, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Planner != nil {
		t.Errorf("the planner block must never load; got %+v", r.Planner)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "planner block is ignored") {
		t.Errorf("expected a warning that the block was ignored; got %v", warns)
	}
	if !strings.Contains(joined, "defaults.planner_kind") {
		t.Errorf("the warning must name the supported alternative; got %v", warns)
	}
	if r.Builder == nil || r.Builder.Kind != "codex" {
		t.Errorf("the builder block must still load; got %+v", r.Builder)
	}
}

func TestLoadRepoWithoutAPlannerBlockDoesNotWarn(t *testing.T) {
	p := writeRepo(t, `{"builder":{"kind":"codex"}}`)
	_, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("absent block must be silent; got %v", warns)
	}
}

func sample(key string) string {
	if key == "args" {
		return `["--dangerously-bypass-approvals-and-sandbox"]`
	}
	if key == "allow_widen" {
		return `["autonomy"]`
	}
	return `"/tmp/evil"`
}

func TestLoadRepoWarnsOnUnknownKey(t *testing.T) {
	p := writeRepo(t, `{"builder":{"kind":"codex","modle":"typo"}}`)
	_, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) == 0 {
		t.Fatal("expected a warning for an unrecognised key")
	}
}

func TestLoadRepoBuilderKindBackCompat(t *testing.T) {
	p := writeRepo(t, `{"builder_kind":"codex"}`)
	r, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.BuilderKind != "codex" {
		t.Fatalf("got %q", r.BuilderKind)
	}
	if len(warns) != 0 {
		t.Fatalf("the legacy key alone must not warn; got %v", warns)
	}
}

func TestLoadRepoWarnsWhenBothKindFormsPresent(t *testing.T) {
	p := writeRepo(t, `{"builder_kind":"codex","builder":{"kind":"opencode"}}`)
	_, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "builder.kind") {
		t.Fatalf("expected a precedence warning; got %v", warns)
	}
}

func TestLoadRepoBuildEngine(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{}`, ""},
		{`{"build_engine":"legacy"}`, "legacy"},
		{`{"build_engine":"durable"}`, "durable"},
	} {
		r, _, err := LoadRepo(writeRepo(t, tc.body))
		if err != nil || r.BuildEngine != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.body, r.BuildEngine, err, tc.want)
		}
	}
}

func TestLoadRepoRefusesAnUnknownBuildEngine(t *testing.T) {
	if _, _, err := LoadRepo(writeRepo(t, `{"build_engine":"durabel"}`)); err == nil || !strings.Contains(err.Error(), "build_engine") {
		t.Fatalf("an unknown build_engine was accepted: %v", err)
	}
}

func TestLoadRepoReviewerBlock(t *testing.T) {
	p := writeRepo(t, `{
	  "builder": {"kind":"copilot"},
	  "reviewer": {"kind":"codex","model":"gpt-6-astra","args":["-c","x"],"bogus":1}
	}`)
	r, warns, err := LoadRepo(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Reviewer == nil || r.Reviewer.Kind != "codex" || r.Reviewer.Model != "gpt-6-astra" {
		t.Fatalf("got reviewer %+v", r.Reviewer)
	}
	if r.Builder == nil || r.Builder.Kind != "copilot" {
		t.Fatalf("the builder block must not be overwritten: %+v", r.Builder)
	}
	all := strings.Join(warns, "\n")
	if !strings.Contains(all, "reviewer.args ignored") || !strings.Contains(all, "reviewer.bogus is not a recognised key") {
		t.Fatalf("got warnings %v", warns)
	}
}
