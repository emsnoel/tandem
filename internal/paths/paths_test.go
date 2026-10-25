package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The three resolvers share one rule, so they are checked as one table: a fresh
// directory resolves to the current name, a legacy-only directory to the legacy
// name, and a directory holding both to the current one.
func TestResolutionPrefersCurrentAndFallsBackToLegacy(t *testing.T) {
	cases := []struct {
		name            string
		resolve         func(string) string
		current, legacy string
	}{
		{"manifest", Manifest, ManifestName, legacyManifestName},
		{"relay", Relay, RelayName, legacyRelayName},
		{"hook baseline", HookBaseline, HookBaselineName, legacyHookBaselineName},
	}
	for _, c := range cases {
		t.Run(c.name+"/empty writes the current name", func(t *testing.T) {
			dir := t.TempDir()
			if got, want := c.resolve(dir), filepath.Join(dir, c.current); got != want {
				t.Errorf("resolve(empty dir) = %q, want %q", got, want)
			}
		})
		t.Run(c.name+"/legacy only keeps the legacy name", func(t *testing.T) {
			dir := t.TempDir()
			touch(t, filepath.Join(dir, c.legacy))
			if got, want := c.resolve(dir), filepath.Join(dir, c.legacy); got != want {
				t.Errorf("resolve(legacy only) = %q, want %q — an existing repo must keep working", got, want)
			}
		})
		t.Run(c.name+"/both prefers the current name", func(t *testing.T) {
			dir := t.TempDir()
			touch(t, filepath.Join(dir, c.legacy))
			touch(t, filepath.Join(dir, c.current))
			if got, want := c.resolve(dir), filepath.Join(dir, c.current); got != want {
				t.Errorf("resolve(both) = %q, want %q", got, want)
			}
		})
	}
}

// The relay is a directory, not a file — the file-based table above would pass
// even if the implementation only ever recognised regular files.
func TestRelayRecognisesADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, legacyRelayName), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := Relay(dir), filepath.Join(dir, legacyRelayName); got != want {
		t.Errorf("Relay(legacy dir) = %q, want %q", got, want)
	}
}

// A broken symlink is something a human put there. Resolving to it makes the
// next read fail loudly; skipping it would silently read a different file.
func TestPresentCountsABrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, ManifestName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, want := Manifest(dir), filepath.Join(dir, ManifestName); got != want {
		t.Errorf("Manifest(broken symlink) = %q, want %q", got, want)
	}
}

func TestCopilotHomePrefersTheEnvironment(t *testing.T) {
	t.Setenv("COPILOT_HOME", "/somewhere/copilot")
	if got := CopilotHome(); got != "/somewhere/copilot" {
		t.Fatalf("got %q, want the COPILOT_HOME value", got)
	}
}

func TestCopilotHomeFallsBackToHome(t *testing.T) {
	t.Setenv("COPILOT_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	if got, want := CopilotHome(), filepath.Join(home, ".copilot"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// monorepo builds boundary/packages/foo (three levels deep) under a fresh
// temp dir and returns (boundary, foo) for NearestManifest tests. Both are
// resolved with EvalSymlinks: NearestManifest now does the same internally
// (see the doc comment on it), so a test that compares its return value
// against an unresolved t.TempDir() path (logical, e.g. under macOS's
// /var/folders, itself a symlink to /private/var/folders) would see a false
// mismatch against NearestManifest's now-always-physical output.
func monorepo(t *testing.T) (boundary, foo string) {
	t.Helper()
	boundary = t.TempDir()
	foo = filepath.Join(boundary, "packages", "foo")
	if err := os.MkdirAll(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(boundary)
	if err != nil {
		t.Fatal(err)
	}
	boundary = resolved
	foo = filepath.Join(boundary, "packages", "foo")
	return boundary, foo
}

func TestNearestManifestPrefersThePackagesOwnManifest(t *testing.T) {
	boundary, foo := monorepo(t)
	touch(t, filepath.Join(boundary, ManifestName))
	touch(t, filepath.Join(foo, ManifestName))
	if got, want := NearestManifest(foo, boundary), filepath.Join(foo, ManifestName); got != want {
		t.Errorf("NearestManifest(foo, boundary) = %q, want the package's own manifest %q", got, want)
	}
}

func TestNearestManifestFallsThroughToTheBoundarysManifest(t *testing.T) {
	boundary, foo := monorepo(t)
	touch(t, filepath.Join(boundary, ManifestName))
	// foo itself has no manifest.
	if got, want := NearestManifest(foo, boundary), filepath.Join(boundary, ManifestName); got != want {
		t.Errorf("NearestManifest(foo, boundary) = %q, want the boundary's manifest %q", got, want)
	}
}

func TestNearestManifestFindsALegacyNamePartway(t *testing.T) {
	boundary, foo := monorepo(t)
	packages := filepath.Dir(foo)
	touch(t, filepath.Join(packages, legacyManifestName))
	if got, want := NearestManifest(foo, boundary), filepath.Join(packages, legacyManifestName); got != want {
		t.Errorf("NearestManifest(foo, boundary) = %q, want the legacy manifest at %q", got, want)
	}
}

func TestNearestManifestFallsBackToTheWritePathWhenNothingExists(t *testing.T) {
	boundary, foo := monorepo(t)
	if got, want := NearestManifest(foo, boundary), filepath.Join(boundary, ManifestName); got != want {
		t.Errorf("NearestManifest(foo, boundary) = %q, want the boundary write path %q", got, want)
	}
}

// A single-directory search (from == boundary) must behave exactly like the
// plain Manifest lookup every other caller still uses.
func TestNearestManifestWithFromEqualToBoundaryMatchesManifest(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // see the monorepo() comment above
	if err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, ManifestName))
	if got, want := NearestManifest(dir, dir), Manifest(dir); got != want {
		t.Errorf("NearestManifest(dir, dir) = %q, want Manifest(dir) = %q", got, want)
	}
}

// A tandem-review round caught this directly: on a symlinked
// checkout, filepath.Abs alone (what cmd/tandem/paths.go computed) keeps the
// LOGICAL path, while vcs.Toplevel (git rev-parse --show-toplevel) always
// returns the PHYSICAL one -- comparing them by simple string equality in
// the walk loop meant "dir == boundary" could never be true, so the walk
// climbed straight past the intended repository root. Reproduced directly: a
// real symlink, a decoy manifest one level above the symlink but OUTSIDE the
// real (physical) repository, and confirming NearestManifest never returns
// it.
func TestNearestManifestResolvesSymlinksBeforeComparingToBoundary(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir()) // see the monorepo() comment above
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(parent, "real")
	foo := filepath.Join(real, "packages", "foo")
	if err := os.MkdirAll(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A decoy manifest OUTSIDE the repo: an ancestor of the symlink's own
	// logical path, but not of the real (physical) repository at all.
	touch(t, filepath.Join(parent, ManifestName))

	logicalFrom := filepath.Join(link, "packages", "foo")
	got := NearestManifest(logicalFrom, real) // real given exactly as vcs.Toplevel would return it: physical
	want := Manifest(real)                    // nothing inside the repo -- correct answer is the write-target fallback at boundary
	if got != want {
		t.Errorf("NearestManifest(logical from, physical boundary) = %q, want %q (must not escape to the decoy at %q)",
			got, want, filepath.Join(parent, ManifestName))
	}
}

func TestIsRelayNameKnowsBothSpellings(t *testing.T) {
	for name, want := range map[string]bool{
		".tandem-relay": true, ".herdr-relay": true,
		"relay": false, ".tandem-relay2": false, "": false, ".tandem.json": false,
	} {
		if got := IsRelayName(name); got != want {
			t.Errorf("IsRelayName(%q) = %v, want %v", name, got, want)
		}
	}
}
