package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFingerprintReturnsPorcelainStatus(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Fingerprint(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "?? new.txt") {
		t.Fatalf("want porcelain status, got %q", got)
	}
}

// Modify tracked files: git status collapses an untracked directory into one
// "?? dir/" line, which would not prove per-file scoping.
func TestFingerprintScopesToGivenPaths(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "test"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "included"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "excluded"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "included", "in.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "excluded", "out.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", "-A"},
		{"-C", dir, "commit", "-q", "-m", "initial"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "included", "in.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "excluded", "out.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Fingerprint(dir, []string{"included"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "included/in.txt") {
		t.Fatalf("want the in-scope tracked change reported, got %q", got)
	}
	if strings.Contains(got, "excluded/out.txt") {
		t.Fatalf("want the out-of-scope tracked change NOT reported, got %q", got)
	}
}
