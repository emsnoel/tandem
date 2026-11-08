package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// startFields splits build start's --fields output and checks there are 14.
func startFields(t *testing.T, out string) []string {
	t.Helper()
	f := strings.Split(out, "\x00")
	if len(f) != 15 || f[14] != "" {
		t.Fatalf("want 14 NUL-terminated fields, got %q", f)
	}
	return f[:14]
}

func TestBuildStartFieldsDurableDryRun(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, out, errOut := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder-kind", "codex",
		"--reviewer", "reviewer", "--reviewer-kind", "claude", "--state-home", e.home, "--dry-run", "--fields")
	if rc != exitcode.OK {
		t.Fatalf("rc %d: %s", rc, errOut)
	}
	if f := startFields(t, out); f[0] != "durable" || f[1] != "" || f[2] != "true" {
		t.Errorf("fields %q", f)
	}
}

// A refusal keeps its exit code and still prints 14 fields.
func TestBuildStartFieldsOnRefusal(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	os.Remove(filepath.Join(paths.Relay(e.root), "reviewer.json"))
	rc, out, _ := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder-kind", "codex", "--state-home", e.home, "--fields")
	if rc != exitcode.ManualGate {
		t.Fatalf("rc %d, want 23", rc)
	}
	if f := startFields(t, out); strings.Join(f, "") != "" {
		t.Errorf("a refusal that wrote nothing gives empty fields, got %q", f)
	}
}

// Review focus 3: when the start answers about a run, run_id carries it. A
// --resume-only start of an active run answers with that run's tick.
func TestBuildStartFieldsCarryTheRun(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, got, errOut := e.start()
	if rc != exitcode.OK {
		t.Fatalf("start: rc %d %s", rc, errOut)
	}
	_, out, _ := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder-kind", "codex",
		"--state-home", e.home, "--resume-only", "--fields")
	if f := startFields(t, out); f[1] != got["run_id"] {
		t.Errorf("run_id %q, want %q", f[1], got["run_id"])
	}
}

func TestBuildStartWithoutFieldsUnchanged(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	rc, out, _ := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder-kind", "codex",
		"--reviewer", "reviewer", "--reviewer-kind", "claude", "--state-home", e.home, "--dry-run")
	if rc != exitcode.OK || !strings.Contains(out, `"engine":"durable"`) {
		t.Errorf("rc %d, out %q", rc, out)
	}
}
