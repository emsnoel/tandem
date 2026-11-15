package pairname

import (
	"os"
	"path/filepath"
	"testing"
)

func noEnv(string) string { return "" }

// clashingWorktrees makes a repository with its main checkout at main/proj and
// linked worktrees at a/proj and b/proj, whose folder slugs clash. It returns
// the two worktrees.
func clashingWorktrees(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main", "proj")
	a, b := filepath.Join(base, "a", "proj"), filepath.Join(base, "b", "proj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "worktree", "add", "-q", "-b", "br-a", a)
	git(t, main, "worktree", "add", "-q", "-b", "br-b", b)
	return a, b
}

// A worktree whose folder name clashes needs the registry's id. The read-only
// form reports that without creating the state home.
func TestResolveReadOnlyWritesNothing(t *testing.T) {
	a, _ := clashingWorktrees(t)
	home := filepath.Join(t.TempDir(), "state")

	got, pending, err := ResolveReadOnly(a, home, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !pending || got.Planner != "planner-"+PendingID {
		t.Fatalf("got %+v pending=%v, want planner-%s pending", got, pending, PendingID)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("state home was created: %v", err)
	}
}

// The main checkout always uses the plain names, so its folder never clashes
// with a linked worktree's.
func TestDeriveIgnoresTheMainCheckoutsFolder(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "a", "repo")
	wt := filepath.Join(base, "b", "repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "worktree", "add", "-q", wt)
	got, err := Derive(wt, filepath.Join(t.TempDir(), "state"))
	if err != nil || got.Planner != "planner-repo" {
		t.Fatalf("got %+v err=%v, want planner-repo", got, err)
	}
}

// A bare repository and a worktree whose folder is gone hold no pair.
func TestDeriveIgnoresBareAndPrunableEntries(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	bare := filepath.Join(base, "a", "repo")
	gone := filepath.Join(base, "c", "repo")
	wt := filepath.Join(base, "b", "repo")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, src, "init", "-q")
	git(t, src, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, base, "clone", "-q", "--bare", src, bare)
	git(t, bare, "worktree", "add", "-q", "-b", "br-c", gone)
	git(t, bare, "worktree", "add", "-q", "-b", "br-b", wt)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	got, err := Derive(wt, filepath.Join(t.TempDir(), "state"))
	if err != nil || got.Planner != "planner-repo" {
		t.Fatalf("got %+v err=%v, want planner-repo", got, err)
	}
}
