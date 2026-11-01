package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveProjectDetection(t *testing.T) {
	root := writeFiles(t, map[string]string{"CONTRIBUTING.md": "", "go.mod": "module x\n"})
	mkdirs(t, root, "plans", "docs/plans")
	got := ResolveProject(root, filepath.Join(root, ".tandem.json"))
	want := Project{
		Root: root, PlansDir: "docs/plans", CheckCommand: "go test ./...", ConventionsFile: "CONTRIBUTING.md",
		BuilderKind: "codex", TaskInputBudget: 3000000, Source: "detected",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestResolveProjectDetectionOrder(t *testing.T) {
	root := writeFiles(t, map[string]string{"AGENTS.md": "", "CONTRIBUTING.md": ""})
	mkdirs(t, root, "docs/superpowers/plans", "docs/plans")
	got := ResolveProject(root, filepath.Join(root, ".tandem.json"))
	if got.PlansDir != "docs/superpowers/plans" || got.ConventionsFile != "AGENTS.md" {
		t.Errorf("got plans %q, conventions %q", got.PlansDir, got.ConventionsFile)
	}
}

func TestResolveProjectManifestOverlay(t *testing.T) {
	root := writeFiles(t, map[string]string{".tandem.json": `{
		"plans_dir": "p", "check_command": "make ci", "check_paths": ["src"], "conventions_file": "C.md",
		"builder_kind": "claude", "prompt_extra": "be brief", "task_input_budget": 5000, "context_limit": 9000,
		"build_engine": "durable", "review_delivery": "mailbox", "acceptance_policy": "a.json", "collaboration": "c1"}`})
	got := ResolveProject(root, filepath.Join(root, ".tandem.json"))
	want := Project{
		Root: root, PlansDir: "p", CheckCommand: "make ci", CheckPaths: []string{"src"}, ConventionsFile: "C.md",
		BuilderKind: "claude", PromptExtra: "be brief", TaskInputBudget: 5000, ContextLimit: 9000,
		BuildEngine: "durable", ReviewDelivery: "mailbox", AcceptancePolicy: "a.json", Collaboration: "c1", Source: "manifest",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestResolveProjectEmptyStringsKeepDetection(t *testing.T) {
	root := writeFiles(t, map[string]string{"AGENTS.md": "", ".tandem.json": `{"plans_dir":"","conventions_file":"","builder_kind":"","prompt_extra":""}`})
	mkdirs(t, root, "plans")
	got := ResolveProject(root, filepath.Join(root, ".tandem.json"))
	if got.PlansDir != "plans" || got.ConventionsFile != "AGENTS.md" || got.BuilderKind != "codex" || got.Source != "manifest" {
		t.Errorf("got %+v", got)
	}
}

func TestResolveProjectNumbers(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{{`5000`, 5000}, {`12.7`, 12}, {`0`, 0}, {`-1`, 3000000}, {`"9"`, 3000000}, {`true`, 3000000}, {`null`, 3000000}}
	for _, c := range cases {
		root := writeFiles(t, map[string]string{".tandem.json": `{"task_input_budget":` + c.raw + `}`})
		if got := ResolveProject(root, filepath.Join(root, ".tandem.json")).TaskInputBudget; got != c.want {
			t.Errorf("task_input_budget %s: got %d, want %d", c.raw, got, c.want)
		}
	}
}

// Review focus 2: a manifest that is not a JSON object warns and changes nothing.
func TestResolveProjectInvalidManifest(t *testing.T) {
	for _, body := range []string{`{"plans_dir":`, `[]`, `null`, `"x"`} {
		root := writeFiles(t, map[string]string{"go.mod": "module x\n", ".tandem.json": body})
		m := filepath.Join(root, ".tandem.json")
		got := ResolveProject(root, m)
		if got.InvalidManifest != m || got.Source != "detected" || got.CheckCommand != "go test ./..." || got.BuilderKind != "codex" {
			t.Errorf("manifest %q: got %+v", body, got)
		}
	}
}

func TestResolveProjectMissingManifestIsNotInvalid(t *testing.T) {
	root := t.TempDir()
	if got := ResolveProject(root, filepath.Join(root, ".tandem.json")); got.InvalidManifest != "" {
		t.Errorf("a missing manifest is not invalid: %+v", got)
	}
}

// tandem-project read each key through $(...), which stripped trailing
// newlines before the empty test.
func TestResolveProjectStripsTrailingNewlines(t *testing.T) {
	root := writeFiles(t, map[string]string{".tandem.json": `{"prompt_extra":"hi\n\n","plans_dir":"\n","check_command":"make\n"}`})
	mkdirs(t, root, "plans")
	got := ResolveProject(root, filepath.Join(root, ".tandem.json"))
	if got.PromptExtra != "hi" || got.PlansDir != "plans" || got.CheckCommand != "make" {
		t.Errorf("got extra %q, plans %q, check %q", got.PromptExtra, got.PlansDir, got.CheckCommand)
	}
}
