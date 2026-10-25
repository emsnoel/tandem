package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
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
	for _, f := range []string{"a.txt", "b.txt", "gone.txt"} {
		write(t, dir, f, "original "+f)
	}
	if err := exec.Command("git", "-C", dir, "add", ".").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-qm", "init").Run(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "-C", dir, "add", "-A").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-qm", "init").Run(); err != nil {
		t.Fatal(err)
	}
}

func TestDirtyHashesSeesModifiedUntrackedAndDeletedFiles(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.txt", "changed")
	write(t, dir, "new/deep/file.txt", "new")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	h, err := DirtyHashes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h["a.txt"] == "" || h["new/deep/file.txt"] == "" || h["gone.txt"] != Deleted {
		t.Fatalf("got %v", h)
	}
	if _, ok := h["b.txt"]; ok {
		t.Errorf("a clean file is listed: %v", h)
	}
}

func TestChangedReportsWhatATaskTouched(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.txt", "dirty before the task")
	before, err := DirtyHashes(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The task edits b.txt, leaves a.txt as it found it, and adds c.txt.
	write(t, dir, "b.txt", "edited by the task")
	write(t, dir, "c.txt", "added by the task")
	after, err := DirtyHashes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Changed(before, after), []string{"b.txt", "c.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Changed = %v, want %v", got, want)
	}
	// Reverting a file that was dirty before is a change too.
	write(t, dir, "a.txt", "original a.txt")
	after, _ = DirtyHashes(dir)
	if got := Changed(before, after); !reflect.DeepEqual(got, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Errorf("after a revert, Changed = %v", got)
	}
}

func TestDirtyHashesReadFailureIsNotDeletion(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.txt", "changed")
	orig := readFile
	readFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
	t.Cleanup(func() { readFile = orig })
	h, err := DirtyHashes(dir)
	if err == nil {
		t.Fatalf("a read failure produced %v; it must abort evidence collection", h)
	}
	if h != nil {
		t.Errorf("partial evidence returned: %v", h)
	}
}

func TestDirtyHashesOnlyAMissingFileIsDeleted(t *testing.T) {
	dir := gitRepo(t)
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	h, err := DirtyHashes(dir)
	if err != nil || h["gone.txt"] != Deleted {
		t.Fatalf("h=%v err=%v", h, err)
	}
}

// Porcelain status says " M" before and after, so only the bytes can tell.
func TestDirtyHashesSeeContentChangesWithIdenticalStatus(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.txt", "first edit")
	before, _ := DirtyHashes(dir)
	write(t, dir, "a.txt", "second edit")
	after, _ := DirtyHashes(dir)
	if got := Changed(before, after); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("Changed = %v", got)
	}
}

func TestDirtyHashesSeeAModeChange(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.txt", "changed")
	before, _ := DirtyHashes(dir)
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, _ := DirtyHashes(dir)
	if got := Changed(before, after); !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("an executable bit flip was not seen: %v", got)
	}
}

// A symlink is hashed by its target text and never followed, so a link out of
// the repository neither leaks that file's bytes nor reads as deleted.
func TestDirtyHashesDoNotFollowSymlinks(t *testing.T) {
	dir := gitRepo(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	before, err := DirtyHashes(dir)
	if err != nil || before["link"] == "" || before["link"] == Deleted {
		t.Fatalf("before=%v err=%v", before, err)
	}
	if err := os.WriteFile(outside, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := DirtyHashes(dir)
	if len(Changed(before, after)) != 0 {
		t.Fatalf("the link target's content changed the hash: %v", Changed(before, after))
	}
	if err := os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	if dangling, err := DirtyHashes(dir); err != nil || dangling["link"] == Deleted {
		t.Fatalf("a dangling link is not a deletion: %v err=%v", dangling, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", link); err != nil {
		t.Fatal(err)
	}
	if retargeted, _ := DirtyHashes(dir); len(Changed(before, retargeted)) != 1 {
		t.Fatalf("retargeting the link was not seen: %v", Changed(before, retargeted))
	}
}
