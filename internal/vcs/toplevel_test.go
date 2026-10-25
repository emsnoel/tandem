package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestToplevelFromNestedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Toplevel(nested)
	if err != nil {
		t.Fatal(err)
	}
	// macOS's /tmp is a symlink to /private/tmp; git resolves it, so compare
	// against a similarly resolved path rather than dir itself.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Toplevel(nested) = %q, want %q", got, want)
	}
}

func TestToplevelOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := Toplevel(dir); err == nil {
		t.Fatal("want an error outside a git repository, got nil")
	}
}
