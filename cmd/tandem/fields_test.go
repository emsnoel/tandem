package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func resolveFields(t *testing.T, args ...string) []string {
	t.Helper()
	out := captureStdout(t, func() int { return cmdConfig(append([]string{"resolve", "--fields"}, args...)) })
	got := fields(out)
	if len(got) != 11 {
		t.Fatalf("got %d fields, want 11: %q", len(got), got)
	}
	return got
}

func TestResolveFieldsReviewerResolved(t *testing.T) {
	dir := withFixture(t, `{"reviewer":{"kind":"codex","model":"gpt-5.4"}}`, "")
	got := resolveFields(t, "--role", "reviewer", "--root", dir)
	if got[0] != "resolved" || got[1] != "" || got[2] != "codex" || got[3] != "gpt-5.4" ||
		got[6] != "repo" || got[7] != "repo" || got[9] == "" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveFieldsReviewerNone(t *testing.T) {
	dir := withFixture(t, `{}`, "")
	t.Setenv("HERDR_REVIEWER_KIND", "")
	got := resolveFields(t, "--role", "reviewer", "--root", dir)
	if got[0] != "none" || got[2] != "" || got[6] != "" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveFieldsReviewerInvalid(t *testing.T) {
	dir := withFixture(t, `{"reviewer":{"model":"x"}}`, "")
	t.Setenv("HERDR_REVIEWER_KIND", "")
	got := resolveFields(t, "--role", "reviewer", "--root", dir)
	if got[0] != "invalid" || got[1] == "" || got[2] != "" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveFieldsBuilderAdoptedUnenforced(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","model":"gpt-5.4"}}`, "")
	got := resolveFields(t, "--role", "builder", "--root", dir, "--adopted", "claude")
	if got[0] != "resolved" || got[2] != "claude" || !strings.Contains(got[10], "model=gpt-5.4") {
		t.Fatalf("got %q", got)
	}
}

// Review focus 4: an argument holding a newline survives.
func TestLaunchSpecFieldsKindThenArgs(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","autonomy":"auto"}}`, `{"adapters":{"codex":{"args":["--x","a\nb"]}}}`)
	if err := config.ApproveKind(config.UserConfigPath(), dir, "codex"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int {
		return cmdLaunchSpec([]string{"--fields", "--role", "builder", "--root", dir, "--cwd", dir})
	})
	got := fields(out)
	if got[0] != "codex" || got[1] != "--sandbox" || got[len(got)-1] != "a\nb" {
		t.Fatalf("got %q", got)
	}
}

func TestLaunchSpecFieldsRefusalPrintsRemedy(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"opencode","autonomy":"readonly"}}`, "")
	if err := config.ApproveKind(config.UserConfigPath(), dir, "opencode"); err != nil {
		t.Fatal(err)
	}
	var rc int
	errOut := captureStderr(t, func() {
		rc = cmdLaunchSpec([]string{"--fields", "--role", "builder", "--root", dir, "--cwd", dir})
	})
	if rc != exitcode.WideningRefused || strings.Contains(errOut, `{"error"`) || !strings.HasPrefix(errOut, "tandem: ") {
		t.Fatalf("rc %d, stderr %q", rc, errOut)
	}
}

func gates() []agent.Action {
	mk := func(gate string, s agent.Status, remedy string) agent.Action {
		return agent.Action{Gate: gate, Status: s, StatusS: s.String(), Detail: gate + " detail", Remedy: remedy}
	}
	return []agent.Action{
		mk("current", agent.StatusCurrent, ""), mk("applied", agent.StatusApplied, "r1"),
		mk("manual", agent.StatusManual, "r2"), mk("warn", agent.StatusWarning, "r3"),
		mk("failed", agent.StatusFailed, "r4"), mk("absent", agent.StatusAbsent, "r5"),
	}
}

func TestPrelaunchFieldsShowNew(t *testing.T) {
	var b strings.Builder
	writeGateFields(&b, gates(), "new")
	got := fields(b.String())
	want := []string{"applied", "applied", "applied detail", "", "failed", "failed", "failed detail", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestPrelaunchFieldsShowAll(t *testing.T) {
	var b strings.Builder
	writeGateFields(&b, gates(), "all")
	got := fields(b.String())
	if len(got) != 20 || got[0] != "applied" || got[8] != "warn" || got[11] != "r3" || got[7] != "" {
		t.Fatalf("got %q", got)
	}
}

func TestPrelaunchShowNeedsFields(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if rc := cmdPrelaunch([]string{"--show", "all", "--kind", "codex", "--cwd", os.TempDir()}); rc != exitcode.Usage {
		t.Fatalf("rc %d", rc)
	}
}

// captureStderr returns what fn writes to stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// A NUL inside a field, say in a builder's report, must not shift the fields
// after it. Command substitution dropped NULs on main, so writeFields does too.
func TestWriteFieldsDropsNUL(t *testing.T) {
	var b strings.Builder
	writeFields(&b, "a\x00b", "c")
	if b.String() != "ab\x00c\x00" {
		t.Errorf("got %q", b.String())
	}
}
