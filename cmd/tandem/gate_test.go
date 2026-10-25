package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGateTeardownRequiresAPhase(t *testing.T) {
	if got := cmdGate([]string{"teardown", "--builder-pane", "p2"}); got != 2 {
		t.Fatalf("want usage error without --phase, got %d", got)
	}
}

func TestGateTeardownRejectsUnknownPhase(t *testing.T) {
	if got := cmdGate([]string{"teardown", "--phase", "middle", "--builder-pane", "p2"}); got != 2 {
		t.Fatalf("want usage error for an unknown phase, got %d", got)
	}
}

// TestGateTeardownScopesTreeFingerprintToCheckPaths proves --check-paths
// actually changes what --phase post's tree comparison catches, by running
// the real cmdGate entrypoint (not a stub) against a real git repo.
func TestGateTeardownScopesTreeFingerprintToCheckPaths(t *testing.T) {
	run := func(t *testing.T, checkPaths []string, mutate func(root string)) int {
		t.Helper()
		root := t.TempDir()
		if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "included"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "excluded"), 0o755); err != nil {
			t.Fatal(err)
		}

		mailboxDir := t.TempDir()
		reportsDir := filepath.Join(mailboxDir, "reports")
		if err := os.MkdirAll(reportsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(reportsDir, "id1.md"), []byte("done"), 0o644); err != nil {
			t.Fatal(err)
		}

		snap := filepath.Join(mailboxDir, ".teardown-snapshot")

		args := func(phase string) []string {
			a := []string{
				"teardown", "--phase", phase,
				"--builder-pane", "p2",
				"--mailbox", mailboxDir,
				"--root", root,
				"--status", "idle",
				"--snapshot", snap,
				"--has-check-command",
				"--verified", "id1",
			}
			for _, p := range checkPaths {
				a = append(a, "--check-paths", p)
			}
			return a
		}

		if rc := cmdGate(args("pre")); rc != 0 {
			t.Fatalf("pre phase: want 0, got %d", rc)
		}

		mutate(root)

		return cmdGate(args("post"))
	}

	writeFile := func(rel string) func(string) {
		return func(root string) {
			if err := os.WriteFile(filepath.Join(root, rel), []byte("x"), 0o644); err != nil {
				panic(err)
			}
		}
	}

	t.Run("no check-paths: a change anywhere trips exit 8", func(t *testing.T) {
		if rc := run(t, nil, writeFile("excluded/out.txt")); rc != 8 {
			t.Fatalf("want 8 (whole-tree scope), got %d", rc)
		}
	})

	t.Run("scoped check-paths: an out-of-scope change is allowed", func(t *testing.T) {
		if rc := run(t, []string{"included"}, writeFile("excluded/out.txt")); rc != 0 {
			t.Fatalf("want 0 (out-of-scope change ignored), got %d", rc)
		}
	})

	t.Run("scoped check-paths: an in-scope change still trips exit 8", func(t *testing.T) {
		if rc := run(t, []string{"included"}, writeFile("included/in.txt")); rc != 8 {
			t.Fatalf("want 8 (in-scope change caught), got %d", rc)
		}
	})
}
