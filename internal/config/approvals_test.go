package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKindApprovedIsPerRepository(t *testing.T) {
	u := &User{Approvals: map[string][]string{"/repo/a": {"opencode"}}}
	if !u.KindApproved("/repo/a", "opencode") {
		t.Error("approved pair must read as approved")
	}
	if u.KindApproved("/repo/a", "droid") {
		t.Error("a different kind in the same repo is not approved")
	}
	if u.KindApproved("/repo/b", "opencode") {
		t.Error("approval must not leak across repositories")
	}
}

func TestKindApprovedEmptyUser(t *testing.T) {
	if (&User{}).KindApproved("/repo/a", "codex") {
		t.Error("a user with no approvals approves nothing")
	}
}

func TestApproveKindPersistsAndIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tandem.json")

	if err := ApproveKind(p, "/repo/a", "opencode"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveKind(p, "/repo/a", "opencode"); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Approvals["/repo/a"]; len(got) != 1 || got[0] != "opencode" {
		t.Fatalf("got %v, want one entry", got)
	}
}

// Approving must not discard the rest of the user's configuration.
func TestApproveKindPreservesOtherSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tandem.json")
	body := `{"defaults":{"builder_kind":"codex"},"adapters":{"codex":{"path":"/usr/bin/codex"}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApproveKind(p, "/repo/a", "opencode"); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Defaults.BuilderKind != "codex" || u.Adapters["codex"].Path != "/usr/bin/codex" {
		t.Fatalf("existing settings were lost: %+v", u)
	}
}

func TestCanonicalKeyResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("filesystem does not support symlinks; nothing to test")
	}

	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks(real): %v", err)
	}
	if got := CanonicalKey(link); got != resolvedReal {
		t.Fatalf("CanonicalKey(%q) = %q, want %q", link, got, resolvedReal)
	}
	if got := CanonicalKey(real); got != resolvedReal {
		t.Fatalf("CanonicalKey(%q) = %q, want %q", real, got, resolvedReal)
	}
}

func TestCanonicalKeyFallsBackOnUnresolvablePath(t *testing.T) {
	if got := CanonicalKey("/repo/a"); got != "/repo/a" {
		t.Fatalf("CanonicalKey(%q) = %q, want the path unchanged when it cannot be resolved", "/repo/a", got)
	}
}

// Approving through one spelling of a symlinked checkout must be visible
// through every other spelling.
func TestApproveKindMatchesThroughSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("filesystem does not support symlinks; nothing to test")
	}

	p := filepath.Join(t.TempDir(), "tandem.json")
	if err := ApproveKind(p, link, "codex"); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if !u.KindApproved(real, "codex") {
		t.Error("approved via the symlink, checked via the resolved path: want approved")
	}
	if !u.KindApproved(link, "codex") {
		t.Error("approved via the symlink, checked via the same symlink: want approved")
	}
}
