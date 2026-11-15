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

func TestPathsNames(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt-b")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", wt},
	} {
		if out, err := exec.Command("git", append([]string{"-C", main}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	home := t.TempDir()
	t.Setenv("HERDR_PLANNER_NAME", "")
	t.Setenv("HERDR_REVIEWER_NAME", "")

	t.Setenv("HERDR_BUILDER_NAME", "")
	out, rc := captureStdoutRC(t, func() int {
		return cmdPaths([]string{"names", "--root", wt, "--state-home", home})
	})
	if rc != 0 || out != "planner-wt-b\tbuilder-wt-b\treviewer-wt-b\n" {
		t.Errorf("rc=%d out=%q", rc, out)
	}

	sub := filepath.Join(wt, "sub")
	relay := filepath.Join(wt, ".tandem-relay")
	if err := os.MkdirAll(relay, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relay, "pair.json"), []byte(`{"reviewer":"rev9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, rc = captureStdoutRC(t, func() int {
		return cmdPaths([]string{"names", "--root", sub, "--state-home", home})
	})
	if rc != 0 || out != "planner-wt-b\tbuilder-wt-b\trev9\n" {
		t.Errorf("subdir: rc=%d out=%q", rc, out)
	}

	t.Setenv("HERDR_BUILDER_NAME", "bob")
	out, rc = captureStdoutRC(t, func() int {
		return cmdPaths([]string{"names", "--root", wt, "--state-home", home})
	})
	if rc != 0 || out != "planner-wt-b\tbob\trev9\n" {
		t.Errorf("rc=%d out=%q", rc, out)
	}
}

// clashingWorktrees makes linked worktrees at a/proj and b/proj, whose folder
// slugs clash, so their names need the registry's checkout id.
func clashingWorktrees(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main", "proj")
	a, b := filepath.Join(base, "a", "proj"), filepath.Join(base, "b", "proj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", "-b", "br-a", a},
		{"worktree", "add", "-q", "-b", "br-b", b},
	} {
		if out, err := exec.Command("git", append([]string{"-C", main}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return a
}
