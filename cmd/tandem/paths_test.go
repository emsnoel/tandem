package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdoutRC is captureStdout for cases that expect a non-zero exit.
func captureStdoutRC(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	rc := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), rc
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatal(err)
	}
}

func TestCmdPathsManifestFromPrefersTheNearestManifest(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	foo := filepath.Join(root, "packages", "foo")
	if err := os.MkdirAll(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	rootManifest := filepath.Join(root, ".tandem.json")
	fooManifest := filepath.Join(foo, ".tandem.json")
	for _, p := range []string{rootManifest, fooManifest} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := strings.TrimSpace(captureStdout(t, func() int {
		return cmdPaths([]string{"manifest", "--from", foo})
	}))
	want, err := filepath.EvalSymlinks(fooManifest)
	if err != nil {
		t.Fatal(err)
	}
	if out != want {
		t.Errorf("got %q, want the package's own manifest %q", out, want)
	}
}

func TestCmdPathsManifestFromFallsThroughToTheToplevel(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	foo := filepath.Join(root, "packages", "foo")
	if err := os.MkdirAll(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	rootManifest := filepath.Join(root, ".tandem.json")
	if err := os.WriteFile(rootManifest, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := strings.TrimSpace(captureStdout(t, func() int {
		return cmdPaths([]string{"manifest", "--from", foo})
	}))
	want, err := filepath.EvalSymlinks(rootManifest)
	if err != nil {
		t.Fatal(err)
	}
	if out != want {
		t.Errorf("got %q, want the toplevel manifest %q", out, want)
	}
}

// Outside a git repository this falls back to paths.Manifest, so the output is
// not symlink-resolved.
func TestCmdPathsManifestFromOutsideAGitRepoActsLikeRoot(t *testing.T) {
	dir := t.TempDir() // deliberately not a git repo
	out := strings.TrimSpace(captureStdout(t, func() int {
		return cmdPaths([]string{"manifest", "--from", dir})
	}))
	want := filepath.Join(dir, ".tandem.json")
	if out != want {
		t.Errorf("got %q, want the single-directory path %q", out, want)
	}
}

func TestCmdPathsManifestRootAndFromAreMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	_, rc := captureStdoutRC(t, func() int {
		return cmdPaths([]string{"manifest", "--root", dir, "--from", dir})
	})
	if rc != 2 { // exitcode.Usage
		t.Fatalf("want usage error (2), got %d", rc)
	}
}

func TestCmdPathsFromRejectedOutsideManifest(t *testing.T) {
	dir := t.TempDir()
	_, rc := captureStdoutRC(t, func() int {
		return cmdPaths([]string{"relay", "--from", dir})
	})
	if rc != 2 { // exitcode.Usage
		t.Fatalf("want usage error (2), got %d", rc)
	}
}

// Without --from, manifest keeps its single-directory behavior.
func TestCmdPathsManifestWithoutFromIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, ".tandem.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSpace(captureStdout(t, func() int {
		return cmdPaths([]string{"manifest", "--root", dir})
	}))
	if out != manifest {
		t.Errorf("got %q, want %q", out, manifest)
	}
}

// On a symlinked checkout, --from must land on the package's own manifest,
// not escape past the physical git toplevel to a decoy outside the repo.
func TestCmdPathsManifestFromResolvesSymlinksAgainstToplevel(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(parent, "real")
	foo := filepath.Join(real, "packages", "foo")
	if err := os.MkdirAll(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, real)
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Decoy manifest outside the repo, one level above the symlink.
	if err := os.WriteFile(filepath.Join(parent, ".tandem.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	logicalFoo := filepath.Join(link, "packages", "foo")
	out := strings.TrimSpace(captureStdout(t, func() int {
		return cmdPaths([]string{"manifest", "--from", logicalFoo})
	}))
	want := filepath.Join(real, ".tandem.json")
	if out != want {
		t.Errorf("got %q, want %q (must not escape to the decoy)", out, want)
	}
}
