package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/doctor"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// okProbe is a machine where Herdr and every agent CLI are present and current.
type okProbe struct{}

func (okProbe) HerdrVersion(context.Context) (string, error)  { return "0.9.3", nil }
func (okProbe) Agents(context.Context) ([]herdr.Agent, error) { return nil, nil }
func (okProbe) Integrations(context.Context) (map[string]herdr.Integration, error) {
	all := map[string]herdr.Integration{}
	for _, k := range []string{"claude", "codex", "copilot", "opencode"} {
		all[k] = herdr.Integration{Kind: k, State: "current"}
	}
	return all, nil
}
func (okProbe) LookPath(bin string) (string, error) { return "/bin/" + bin, nil }
func (okProbe) CLIVersion(_ context.Context, bin string) (string, error) {
	return bin + " 1.0", nil
}

// doctorRepo isolates every config doctor reads, swaps in okProbe, and returns
// a git repo whose .tandem.json is manifest.
func doctorRepo(t *testing.T, manifest string) string {
	t.Helper()
	for _, k := range []string{"HOME", "CODEX_HOME", "COPILOT_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "tandem.json"))
	t.Setenv("HERDR_ENV", "1")
	for _, k := range []string{"HERDR_BUILDER_KIND", "HERDR_REVIEWER_KIND", "HERDR_STRICT_HOOK_CHECK", "HERDR_SKIP_HOOK_CHECK"} {
		t.Setenv(k, "")
	}
	old := doctorProbe
	doctorProbe = okProbe{}
	t.Cleanup(func() { doctorProbe = old })
	root := t.TempDir()
	initGitRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, ".tandem.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// claude as planner, builder and reviewer, with its trust dialog accepted:
// everything passes.
func healthyRepo(t *testing.T) string {
	t.Helper()
	root := doctorRepo(t, `{"reviewer": {"kind": "claude"}}`)
	key, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"projects":{"` + key + `":{"hasTrustDialogAccepted":true}}}`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDoctorExit0WhenHealthy(t *testing.T) {
	root := healthyRepo(t)
	out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude"}) })
	if rc != exitcode.OK {
		t.Fatalf("got exit %d, want 0\n%s", rc, out)
	}
}

func TestDoctorExit0WithOnlyWarnings(t *testing.T) {
	root := healthyRepo(t)
	t.Setenv("HERDR_ENV", "")
	out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude"}) })
	if rc != exitcode.OK || !strings.Contains(out, "warn") {
		t.Fatalf("got exit %d, want 0 with a warning\n%s", rc, out)
	}
}

func TestDoctorExit30OnAnyFail(t *testing.T) {
	root := doctorRepo(t, `{"builder": {"kind": "codex"}}`)
	out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root}) })
	if rc != exitcode.DoctorFailed || exitcode.DoctorFailed != 30 {
		t.Fatalf("got exit %d, want 30\n%s", rc, out)
	}
}

func TestDoctorTextShowsFixUnderFail(t *testing.T) {
	root := doctorRepo(t, `{"builder": {"kind": "codex"}, "reviewer": {"kind": "codex"}}`)
	out, _ := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root}) })
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "  FAIL  ") && strings.Contains(l, "builder kind codex") {
			if i+1 >= len(lines) || lines[i+1] != "        fix: tandem-pair --approve-kind codex" {
				t.Fatalf("no fix under the fail:\n%s", out)
			}
			return
		}
	}
	t.Fatalf("no approval fail line:\n%s", out)
}

func TestDoctorTextGroupsAndSummary(t *testing.T) {
	root := doctorRepo(t, `{"builder": {"kind": "codex"}}`)
	out, _ := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root}) })
	for _, want := range []string{"tandem doctor  (repo: ", "\nMachine\n", "\nRepo\n", "config: .tandem.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	last := strings.Split(strings.TrimSpace(out), "\n")
	if got := last[len(last)-1]; !strings.HasPrefix(got, "2 failures") {
		t.Errorf("summary line %q, want it to count 2 failures", got)
	}
}

func TestDoctorJSONShape(t *testing.T) {
	root := doctorRepo(t, `{"builder": {"kind": "codex"}}`)
	out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--json"}) })
	var doc struct {
		Root     string          `json:"root"`
		Manifest string          `json:"manifest"`
		Results  []doctor.Result `json:"results"`
		Failures int             `json:"failures"`
		Warnings int             `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if rc != exitcode.DoctorFailed || doc.Failures != 2 || len(doc.Results) == 0 || doc.Manifest == "" {
		t.Fatalf("rc %d, doc %+v", rc, doc)
	}
	r := doc.Results[0]
	if r.Group != "machine" || r.Name == "" || r.Status == "" || r.Detail == "" {
		t.Fatalf("first result %+v", r)
	}
}

// Review focus 4: run from deep inside the repo, doctor still finds the
// manifest at the git toplevel.
func TestDoctorFromSubdirectoryFindsManifest(t *testing.T) {
	root := doctorRepo(t, `{"builder": {"kind": "codex"}}`)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _ := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", sub, "--json"}) })
	var doc struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(root, ".tandem.json"))
	got, _ := filepath.EvalSymlinks(doc.Manifest)
	if got != want {
		t.Fatalf("manifest %q, want %q", doc.Manifest, want)
	}
}

// Review focus 3: a broken manifest is a reported failure, not a crash.
func TestDoctorMalformedManifest(t *testing.T) {
	root := doctorRepo(t, `{"builder": `)
	out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root}) })
	if rc != exitcode.DoctorFailed || !strings.Contains(out, ".tandem.json") {
		t.Fatalf("got exit %d\n%s", rc, out)
	}
}

func TestDoctorUnknownFlagIsUsage(t *testing.T) {
	if rc := cmdDoctor([]string{"--nope"}); rc != exitcode.Usage {
		t.Fatalf("got exit %d, want 2", rc)
	}
}

// A long fix wraps under its own indent instead of running off the screen.
func TestDoctorWrapsLongLines(t *testing.T) {
	var b strings.Builder
	long := strings.Repeat("word ", 60)
	printDoctor(&b, doctorReport{Root: "/r", Results: []doctor.Result{
		{Group: "repo", Name: "x", Status: doctor.Warn, Detail: "short", Fix: long},
	}})
	for _, l := range strings.Split(b.String(), "\n") {
		if len(l) > 100 {
			t.Errorf("line of %d columns: %q", len(l), l)
		}
		if strings.HasPrefix(l, "        ") && !strings.HasPrefix(l, "        fix: ") &&
			!strings.HasPrefix(l, "             word") {
			t.Errorf("continuation not aligned under the fix text: %q", l)
		}
	}
}
