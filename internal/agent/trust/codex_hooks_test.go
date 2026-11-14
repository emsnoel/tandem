package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

func TestCodexHooksCleanWhenNoSources(t *testing.T) {
	codexHome(t, "")
	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	if got == nil {
		t.Fatal("no hook-acceptance action")
	}
	if got.Disposition != agent.Report {
		t.Errorf("this gate must never write; got %v", got.Disposition)
	}
	if got.Status != agent.StatusCurrent {
		t.Fatalf("no hook sources means nothing to accept; got %v", got.Status)
	}
}

// unacceptedHook gives a CODEX_HOME with one hook source and no hooks.state
// entry, as for a plugin whose hooks nobody has accepted.
func unacceptedHook(t *testing.T) {
	t.Helper()
	home := codexHome(t, "")
	body := []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`)
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The finding is a guess from Codex's files, and it has been wrong both ways
// (F29, F31). It warns and never refuses, for any role.
func TestCodexHooksWarnsAboutAnUnacceptedSource(t *testing.T) {
	unacceptedHook(t)

	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	if got.Status != agent.StatusWarning || got.StatusS != "warning" {
		t.Fatalf("an unaccepted hook source must warn; got %v %q (%s)", got.Status, got.StatusS, got.Detail)
	}
	for _, role := range []agent.Role{agent.RolePlanner, agent.RoleBuilder, agent.RoleReviewer} {
		if Blocks(role, *got) {
			t.Errorf("a guess must not stop a %s from launching", role)
		}
	}
}

// The remedy names what was measured on each version, and every way out.
func TestCodexHooksRemedyNamesTheMeasuredVersionsAndTheWaysOut(t *testing.T) {
	unacceptedHook(t)

	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	for _, want := range []string{"0.167.0", "0.151.0", "0.152.1", "/hooks", "--dangerously-bypass-hook-trust", "can be wrong"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("the remedy does not name %q: %q", want, got.Remedy)
		}
	}
}

// With a codex on PATH, the remedy also names the installed version, so a
// reader can judge whether it behaves the same.
func TestCodexHooksRemedyNamesTheInstalledVersionWhenReadable(t *testing.T) {
	unacceptedHook(t)

	bin := t.TempDir()
	script := "#!/bin/sh\necho 'codex-cli 9.9.9-test'\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	if !strings.Contains(got.Remedy, "9.9.9-test") {
		t.Fatalf("remedy must name the installed version read from codex --version; got %q", got.Remedy)
	}
}

// Advisory only: a version tandem cannot read must never blank out the
// remedy or change the gate's verdict, only omit itself from the message.
func TestCodexHooksRemedyDegradesWhenVersionUnreadable(t *testing.T) {
	unacceptedHook(t)

	bin := t.TempDir() // deliberately empty: no codex binary on PATH
	t.Setenv("PATH", bin)

	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	if got.Status != agent.StatusWarning {
		t.Fatalf("an unreadable version must not change the verdict; got %v", got.Status)
	}
	if got.Remedy == "" {
		t.Fatal("a missing codex binary must not blank out the remedy")
	}
	if !strings.Contains(got.Remedy, "could not be read") {
		t.Fatalf("remedy must say the version could not be read; got %q", got.Remedy)
	}
}

// A dry run leaves Codex's home as it found it: no hook baseline is recorded.
func TestCodexHooksDryRunRecordsNoBaseline(t *testing.T) {
	home := t.TempDir()
	hooksFile := filepath.Join(home, "hooks.json")
	if err := os.WriteFile(hooksFile, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := `[hooks.state."` + hooksFile + `:session_start:0:0"]` + "\ntrusted_hash = \"sha256:aaa\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	before, _ := os.ReadDir(home)
	CheckWithOptions("codex", t.TempDir(), agent.RoleBuilder, Options{DryRun: true})
	after, _ := os.ReadDir(home)
	if len(after) != len(before) {
		t.Fatalf("dry run changed CODEX_HOME: %d entries before, %d after", len(before), len(after))
	}
}
