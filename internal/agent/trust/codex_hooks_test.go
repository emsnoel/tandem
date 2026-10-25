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
	body := []byte(`{"hooks":{"SessionStart":[]}}`)
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Codex silently skips an unaccepted hook rather than prompting, so it cannot
// stall an unattended pane. The gate warns and does not refuse.
func TestCodexHooksWarnsAboutUnacceptedSource(t *testing.T) {
	unacceptedHook(t)

	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	if got.Status != agent.StatusWarning {
		t.Fatalf("an unaccepted hook source must warn, not refuse; got %v (%s)", got.Status, got.Detail)
	}
	if got.StatusS != "warning" {
		t.Errorf("the wire form must say warning; got %q", got.StatusS)
	}
	if got.Remedy == "" {
		t.Error("a warning must still carry the remedy that clears it")
	}
}

// Older Codex versions prompted, invisibly to Herdr, so
// HERDR_STRICT_HOOK_CHECK keeps the refusal available. The caller chooses the
// policy.
func TestCodexHooksRefusesUnacceptedSourceUnderStrictPolicy(t *testing.T) {
	unacceptedHook(t)

	actions := CheckWithOptions("codex", t.TempDir(), agent.RoleBuilder, Options{StrictHooks: true})
	got := find(actions, "hook-acceptance")
	if got.Status != agent.StatusManual {
		t.Fatalf("strict policy must refuse; got %v (%s)", got.Status, got.Detail)
	}
	if got.Remedy == "" {
		t.Error("a Manual status must carry a remedy")
	}
}

// The warning always names the versions the silent skip was confirmed on.
func TestCodexHooksWarningNamesTheMeasuredBaseline(t *testing.T) {
	unacceptedHook(t)

	got := find(Check("codex", t.TempDir(), agent.RoleBuilder), "hook-acceptance")
	if !strings.Contains(got.Remedy, "0.151.0") || !strings.Contains(got.Remedy, "0.152.1") {
		t.Fatalf("remedy must name the measured baseline versions; got %q", got.Remedy)
	}
}

// With a codex on PATH, the warning also names the installed version, so a
// reader can judge whether it behaves the same.
func TestCodexHooksWarningNamesTheInstalledVersionWhenReadable(t *testing.T) {
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
func TestCodexHooksWarningDegradesWhenVersionUnreadable(t *testing.T) {
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
