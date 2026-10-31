package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// unacceptedClaudeHome gives a home where claude's trust dialog is NOT accepted,
// which is the canonical Manual gate.
func unacceptedClaudeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func TestPrelaunchBuilderRefusesManualGate(t *testing.T) {
	unacceptedClaudeHome(t)
	rc := cmdPrelaunch([]string{"--role", "builder", "--kind", "claude", "--cwd", t.TempDir()})
	if rc != 23 {
		t.Fatalf("got exit %d, want 23", rc)
	}
}

// The reviewer runs unattended too, so it is gated like the builder.
func TestPrelaunchReviewerRefusesManualGate(t *testing.T) {
	unacceptedClaudeHome(t)
	rc := cmdPrelaunch([]string{"--role", "reviewer", "--kind", "claude", "--cwd", t.TempDir()})
	if rc != 23 {
		t.Fatalf("got exit %d, want 23", rc)
	}
}

// The identical finding is only a note for a planner: a human is present.
func TestPrelaunchPlannerToleratesManualGate(t *testing.T) {
	unacceptedClaudeHome(t)
	rc := cmdPrelaunch([]string{"--role", "planner", "--kind", "claude", "--cwd", t.TempDir()})
	if rc != 0 {
		t.Fatalf("got exit %d, want 0", rc)
	}
}

func TestPrelaunchEmitsActionsAsJSON(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	out := captureStdout(t, func() int {
		return cmdPrelaunch([]string{"--role", "builder", "--kind", "codex", "--cwd", t.TempDir()})
	})
	var doc struct {
		Actions []struct {
			Gate        string `json:"gate"`
			Disposition string `json:"disposition"`
			Status      string `json:"status"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(doc.Actions) != 2 {
		t.Fatalf("codex has two gates; got %d", len(doc.Actions))
	}
	for _, a := range doc.Actions {
		if a.Gate == "" || a.Status == "" || a.Disposition == "" {
			t.Errorf("incomplete action: %+v", a)
		}
	}
}

// opencode has no launch-time gate at all; that is a clean pass, not an error.
func TestPrelaunchOpenCodeHasNoGates(t *testing.T) {
	rc := cmdPrelaunch([]string{"--role", "builder", "--kind", "opencode", "--cwd", t.TempDir()})
	if rc != 0 {
		t.Fatalf("got exit %d, want 0", rc)
	}
}

// codexHomeWithUnacceptedHook gives a CODEX_HOME whose one hook source has no
// hooks.state entry. HERDR_TANDEM_CONFIG is redirected so the resolution
// prelaunch performs cannot read the operator's real user config.
func codexHomeWithUnacceptedHook(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for name, body := range map[string]string{
		"config.toml": "\n",
		"hooks.json":  `{"hooks":{"SessionStart":[]}}`,
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "tandem.json"))
}

// Codex skips an unaccepted hook silently rather than prompting, so it cannot
// stall the pane. Pairing continues with a warning.
func TestPrelaunchBuilderWarnsAboutUnacceptedHooks(t *testing.T) {
	codexHomeWithUnacceptedHook(t)
	rc := cmdPrelaunch([]string{"--role", "builder", "--kind", "codex", "--cwd", t.TempDir()})
	if rc != 0 {
		t.Fatalf("got exit %d, want 0", rc)
	}
}

func TestPrelaunchBuilderRefusesUnacceptedHooksUnderStrictPolicy(t *testing.T) {
	codexHomeWithUnacceptedHook(t)
	t.Setenv("HERDR_STRICT_HOOK_CHECK", "1")
	rc := cmdPrelaunch([]string{"--role", "builder", "--kind", "codex", "--cwd", t.TempDir()})
	if rc != 23 {
		t.Fatalf("got exit %d, want 23", rc)
	}
}

// A kind the user named on tandem-pair's command line is their own choice,
// even when .tandem.json asks for the same kind. launch-spec and doctor agree,
// so prelaunch must not refuse it as unapproved.
func TestPrelaunchUserNamedKindIsAuthorised(t *testing.T) {
	for _, k := range []string{"HOME", "COPILOT_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "tandem.json"))
	t.Setenv("HERDR_BUILDER_KIND", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".tandem.json"), []byte(`{"builder":{"kind":"copilot"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--role", "builder", "--kind", "copilot", "--root", root, "--cwd", root, "--dry-run"}
	if _, rc := captureStdoutRC(t, func() int { return cmdPrelaunch(args) }); rc != 23 {
		t.Fatalf("repo-chosen, unapproved: got exit %d, want 23", rc)
	}
	args = append(args, "--user-kind", "copilot")
	if out, rc := captureStdoutRC(t, func() int { return cmdPrelaunch(args) }); rc != 0 {
		t.Fatalf("user-named: got exit %d, want 0\n%s", rc, out)
	}
}
