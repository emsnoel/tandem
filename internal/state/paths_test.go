package state

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repoFixture makes a committed repository; its path is not symlink-resolved,
// so on macOS it begins with the /var alias of /private/var.
func repoFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func home(t *testing.T) string { return filepath.Join(t.TempDir(), "state-home") }

func TestStateIdentitySeparatesWorktrees(t *testing.T) {
	repo := repoFixture(t)
	wt := filepath.Join(filepath.Dir(repo), "second-worktree")
	git(t, repo, "worktree", "add", "-q", wt, "-b", "other")
	h := home(t)

	a, err := ResolveIdentity(repo, h)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveIdentity(wt, h)
	if err != nil {
		t.Fatal(err)
	}
	if a.CheckoutID == b.CheckoutID || a.StateRoot == b.StateRoot {
		t.Fatalf("two worktrees share state: %+v %+v", a, b)
	}
	if a.GitCommonDir != b.GitCommonDir {
		t.Fatalf("the common dir should be shared: %q vs %q", a.GitCommonDir, b.GitCommonDir)
	}
	if a.CanonicalRoot == b.CanonicalRoot {
		t.Fatal("the worktree roots must differ")
	}

	// Aliases of one root are one checkout: a symlink, a trailing dot, a subdirectory.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(repo, "sub", "dir"), 0o755)
	for name, cwd := range map[string]string{
		"symlink":      alias,
		"dotted":       filepath.Join(repo, "sub", ".."),
		"subdirectory": filepath.Join(repo, "sub", "dir"),
		"through link": filepath.Join(alias, "sub"),
	} {
		got, err := ResolveIdentity(cwd, h)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.CheckoutID != a.CheckoutID || got.CanonicalRoot != a.CanonicalRoot {
			t.Errorf("%s resolved to %+v, want checkout %s", name, got, a.CheckoutID)
		}
	}
	if real, _ := filepath.EvalSymlinks(repo); a.CanonicalRoot != real {
		t.Errorf("canonical root %q, want the real path %q", a.CanonicalRoot, real)
	}
}

func TestStateIdentityKeepsMonorepoScopesInOneCheckout(t *testing.T) {
	repo := repoFixture(t)
	for _, pkg := range []string{"pkg/a/src", "pkg/b"} {
		os.MkdirAll(filepath.Join(repo, pkg), 0o755)
	}
	os.WriteFile(filepath.Join(repo, "pkg", "a", ".tandem.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(repo, "pkg", "b", ".tandem.json"), []byte("{}"), 0o644)
	h := home(t)
	a, err := ResolveIdentity(filepath.Join(repo, "pkg", "a", "src"), h)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ResolveIdentity(filepath.Join(repo, "pkg", "b"), h)
	top, _ := ResolveIdentity(repo, h)
	if a.CheckoutID != b.CheckoutID || a.CheckoutID != top.CheckoutID {
		t.Fatal("packages of one checkout got different state roots")
	}
	if a.ManifestRoot != "pkg/a" || b.ManifestRoot != "pkg/b" || top.ManifestRoot != "." {
		t.Fatalf("scopes %q %q %q", a.ManifestRoot, b.ManifestRoot, top.ManifestRoot)
	}
}

func TestResolveRootIsUnderTheStateHomeAndStable(t *testing.T) {
	repo := repoFixture(t)
	h := home(t)
	root, err := ResolveRoot(repo, h)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ResolveIdentity(repo, h)
	if root != id.StateRoot || filepath.Dir(root) != resolvedHome(t, h) {
		t.Fatalf("root %q, identity %+v", root, id)
	}
	if len(id.CheckoutID) != 32 || strings.Trim(id.CheckoutID, "0123456789abcdef") != "" {
		t.Fatalf("checkout id %q is not 32 lowercase hex characters", id.CheckoutID)
	}
	again, _ := ResolveRoot(repo, h)
	if again != root {
		t.Fatal("the same checkout resolved to a different root")
	}
	// Another state home is a different base, not a different checkout name.
	other, _ := ResolveIdentity(repo, home(t))
	if other.CheckoutID == id.CheckoutID {
		t.Fatal("two state homes shared a random id")
	}
}

func resolvedHome(t *testing.T, h string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(h)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestDefaultStateHomeIsUnderTheUserConfigDir(t *testing.T) {
	got, err := stateHome("", func() (string, error) { return "/cfg", nil })
	if err != nil || got != filepath.Join("/cfg", "tandem", "state") {
		t.Fatalf("got %q, %v", got, err)
	}
	got, _ = stateHome("/explicit/base", func() (string, error) { return "/cfg", nil })
	if got != "/explicit/base" {
		t.Fatalf("override %q replaces the whole base", got)
	}
	if _, err := stateHome("", func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Fatal("an unresolvable config dir must be an error, not a relative path")
	}
}

func TestStateRootsInsideTheRelayOrCheckoutAreRejected(t *testing.T) {
	repo := repoFixture(t)
	alias := filepath.Join(t.TempDir(), "relay-alias")
	os.MkdirAll(filepath.Join(repo, ".tandem-relay"), 0o755)
	if err := os.Symlink(filepath.Join(repo, ".tandem-relay"), alias); err != nil {
		t.Fatal(err)
	}
	// Outside the checkout, so only the relay-name rule can reject these.
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(elsewhere, ".tandem-relay"), 0o755)
	relayAlias := filepath.Join(t.TempDir(), "alias-outside")
	if err := os.Symlink(filepath.Join(elsewhere, ".tandem-relay"), relayAlias); err != nil {
		t.Fatal(err)
	}
	for name, override := range map[string]string{
		"relay name outside the checkout":        filepath.Join(elsewhere, ".tandem-relay", "state"),
		"legacy relay name outside the checkout": filepath.Join(elsewhere, "x", ".herdr-relay"),
		"symlink to an outside relay":            filepath.Join(relayAlias, "state"),
		"current relay name":                     filepath.Join(repo, ".tandem-relay", "state"),
		"legacy relay name":                      filepath.Join(repo, ".herdr-relay"),
		"nested under a relay":                   filepath.Join(repo, "sub", ".tandem-relay", "a", "b"),
		"symlink into the relay":                 filepath.Join(alias, "state"),
		"inside the checkout":                    filepath.Join(repo, "build", "state"),
	} {
		_, err := ResolveRoot(repo, override)
		if !errors.Is(err, ErrUnsafeRoot) {
			t.Errorf("%s: err = %v, want ErrUnsafeRoot", name, err)
		}
	}
	// Nothing was created for a rejected root.
	if _, err := os.Stat(filepath.Join(repo, "build")); err == nil {
		t.Error("a rejected root was created")
	}
}

func TestResolveRootNeedsAGitCheckout(t *testing.T) {
	if _, err := ResolveRoot(t.TempDir(), home(t)); err == nil {
		t.Fatal("a directory outside any checkout was given a state root")
	}
}

func readRegistry(t *testing.T, h string) registryFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r registryFile
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistryIsPrivateAtomicAndOneEntryPerCheckout(t *testing.T) {
	repo := repoFixture(t)
	h := home(t)
	ResolveRoot(repo, h)
	ResolveRoot(filepath.Join(repo, "."), h)
	r := readRegistry(t, h)
	if len(r.Checkouts) != 1 {
		t.Fatalf("registry %+v", r)
	}
	info, _ := os.Stat(filepath.Join(h, "registry.json"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("registry mode %v, want 0600", info.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(h, "registry.json.*")); len(left) != 0 {
		t.Errorf("temporary registry files left: %v", left)
	}
}

func TestRegistryAmbiguousBindingsAreErrors(t *testing.T) {
	for name, mutate := range map[string]func(r *registryFile){
		"duplicate root": func(r *registryFile) {
			r.Checkouts = append(r.Checkouts, r.Checkouts[0])
			r.Checkouts[1].ID = strings.Repeat("b", 32)
		},
		"duplicate id": func(r *registryFile) {
			r.Checkouts = append(r.Checkouts, r.Checkouts[0])
			r.Checkouts[1].CanonicalRoot = "/elsewhere"
		},
		"common dir changed": func(r *registryFile) { r.Checkouts[0].GitCommonDir = "/some/other/.git" },
	} {
		t.Run(name, func(t *testing.T) {
			repo := repoFixture(t)
			h := home(t)
			ResolveRoot(repo, h)
			r := readRegistry(t, h)
			mutate(&r)
			raw, _ := json.Marshal(r)
			os.WriteFile(filepath.Join(h, "registry.json"), raw, 0o600)
			if _, err := ResolveRoot(repo, h); !errors.Is(err, workflow.ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
		})
	}
	t.Run("corrupt file", func(t *testing.T) {
		repo := repoFixture(t)
		h := home(t)
		ResolveRoot(repo, h)
		os.WriteFile(filepath.Join(h, "registry.json"), []byte("{not json"), 0o600)
		if _, err := ResolveRoot(repo, h); err == nil {
			t.Fatal("a corrupt registry was replaced silently")
		}
	})
}

func TestRegistryConcurrentFirstResolvesAgreeOnOneID(t *testing.T) {
	repo := repoFixture(t)
	h := home(t)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := ResolveIdentity(repo, h)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id.CheckoutID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("racing first resolves produced %d ids: %v", len(seen), seen)
	}
	if n := len(readRegistry(t, h).Checkouts); n != 1 {
		t.Fatalf("registry has %d entries", n)
	}
}

func TestGitIdentityOfARelativeRootMatchesTheAbsoluteOne(t *testing.T) {
	repo := repoFixture(t)
	wantRoot, wantCommon, err := gitIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	wantScope, err := manifestScope(repo, wantRoot)
	if err != nil {
		t.Fatal(err)
	}
	if scope, err := manifestScope(".", wantRoot); err != nil || scope != wantScope {
		t.Fatalf("relative manifest scope %q (%v), absolute %q", scope, err, wantScope)
	}
	root, common, err := gitIdentity(".")
	if err != nil {
		t.Fatal(err)
	}
	if root != wantRoot || common != wantCommon {
		t.Fatalf("relative root gave (%s, %s), absolute gave (%s, %s)", root, common, wantRoot, wantCommon)
	}
	if !filepath.IsAbs(common) {
		t.Fatalf("git dir %q is not absolute", common)
	}
}
