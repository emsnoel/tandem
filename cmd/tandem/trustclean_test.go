package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent/trust"
)

func TestTrustCleanRemovesOnlyMarkedEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	repo := t.TempDir()
	key, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(home, "config.toml")
	body := "# " + trust.Marker + "\n[projects.\"" + key + "\"]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() int {
		return cmdTrust([]string{"clean", "--kind", "codex", "--cwd", repo})
	})
	var doc struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !doc.Removed {
		t.Fatal("a marked entry must be reported as removed")
	}
	after, _ := os.ReadFile(cfg)
	if strings.Contains(string(after), repo) || strings.Contains(string(after), key) {
		t.Errorf("entry should be gone; got:\n%s", after)
	}
}

// An entry the user wrote, or one written before markers existed, is theirs.
func TestTrustCleanLeavesUnmarkedEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	repo := t.TempDir()
	key, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(home, "config.toml")
	body := "[projects.\"" + key + "\"]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() int {
		return cmdTrust([]string{"clean", "--kind", "codex", "--cwd", repo})
	})
	if strings.Contains(out, "true") {
		t.Error("an unmarked entry must not be removed")
	}
	after, _ := os.ReadFile(cfg)
	if string(after) != body {
		t.Errorf("the file must be byte-identical; got:\n%s", after)
	}
}

func TestTrustCleanMissingConfigIsNotAnError(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if rc := cmdTrust([]string{"clean", "--kind", "codex", "--cwd", t.TempDir()}); rc != 0 {
		t.Fatalf("got exit %d, want 0", rc)
	}
}
