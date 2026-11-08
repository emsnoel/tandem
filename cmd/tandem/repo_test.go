package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/paths"
)

const threeTaskPlan = "# Greeting fixes\n\n**Goal:** greet.\n\n### Task 1: Punctuate\n\none\n\n### Task 2: Farewell\n\ntwo\n\n### Task 3: Review\n\nthree\n"

// pairedRepo makes a git repo whose relay records a reviewer pane, as
// tandem-pair leaves it.
func pairedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", root},
		{"-C", root, "config", "user.email", "t@example.com"},
		{"-C", root, "config", "user.name", "t"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	// The relay is ignored in a real pairing (tandem-pair adds it).
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".tandem-relay/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	relay := paths.Relay(root)
	if err := os.MkdirAll(relay, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relay, "reviewer.json"), []byte(`{"kind":"claude","name":"reviewer","model":"","autonomy":"auto","model_measured":false}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
