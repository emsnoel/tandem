package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/workflow"
)

// ErrUnsafeRoot is returned for a state home that a relay teardown, a
// `git clean` or a checkout switch could delete: one inside a relay directory
// or inside the checkout it serves.
var ErrUnsafeRoot = errors.New("unsafe state root")

// Identity says which durable state belongs to a directory.
type Identity struct {
	// CheckoutID is a random 32 character hex id bound in the registry to the
	// canonical worktree root. Two worktrees of one repository are two checkouts.
	CheckoutID string `json:"checkout_id"`
	// CanonicalRoot is the worktree's git toplevel with symlinks resolved.
	CanonicalRoot string `json:"canonical_root"`
	// GitCommonDir is the resolved common .git directory, shared by worktrees.
	GitCommonDir string `json:"git_common_dir"`
	// ManifestRoot is the nearest .tandem.json directory below the checkout
	// root, relative to it ("." for the root). It scopes a project within the
	// checkout and never changes the checkout's identity.
	ManifestRoot string `json:"manifest_root"`
	// StateRoot is <state home>/<checkout id>; the database is state.sqlite in it.
	StateRoot string `json:"state_root"`
}

// ResolveRoot returns the state root for the checkout containing cwd. override,
// when not empty, replaces the state home (the base directory), not the
// per-checkout directory.
func ResolveRoot(cwd, override string) (string, error) {
	id, err := ResolveIdentity(cwd, override)
	if err != nil {
		return "", err
	}
	return id.StateRoot, nil
}

// ResolveIdentity canonicalizes cwd to a Git worktree, finds its manifest
// scope, and binds the worktree to a checkout id in the registry, creating the
// binding on first use.
func ResolveIdentity(cwd, override string) (Identity, error) {
	home, err := stateHome(override, os.UserConfigDir)
	if err != nil {
		return Identity{}, err
	}
	root, common, err := gitIdentity(cwd)
	if err != nil {
		return Identity{}, err
	}
	realHome, err := checkHome(home, root)
	if err != nil {
		return Identity{}, err
	}
	scope, err := manifestScope(cwd, root)
	if err != nil {
		return Identity{}, err
	}
	id, err := bind(realHome, root, common)
	if err != nil {
		return Identity{}, err
	}
	return Identity{CheckoutID: id, CanonicalRoot: root, GitCommonDir: common, ManifestRoot: scope,
		StateRoot: filepath.Join(realHome, id)}, nil
}

// LookupIdentity is ResolveIdentity without any write. It never creates the
// state home, the registry or a binding; Bound is false when this worktree has
// none yet, and CheckoutID and StateRoot are then empty. Read-only commands
// such as `state status` use it.
func LookupIdentity(cwd, override string) (id Identity, bound bool, err error) {
	home, err := stateHome(override, os.UserConfigDir)
	if err != nil {
		return Identity{}, false, err
	}
	root, common, err := gitIdentity(cwd)
	if err != nil {
		return Identity{}, false, err
	}
	realHome, err := checkHome(home, root)
	if err != nil {
		return Identity{}, false, err
	}
	scope, err := manifestScope(cwd, root)
	if err != nil {
		return Identity{}, false, err
	}
	id = Identity{CanonicalRoot: root, GitCommonDir: common, ManifestRoot: scope}
	raw, err := os.ReadFile(filepath.Join(realHome, registryName))
	if errors.Is(err, fs.ErrNotExist) {
		return id, false, nil
	}
	if err != nil {
		return id, false, err
	}
	var reg registryFile
	if err := json.Unmarshal(raw, &reg); err != nil {
		return id, false, fmt.Errorf("the state registry is corrupt: %w", err)
	}
	for _, e := range reg.Checkouts {
		if e.CanonicalRoot != root {
			continue
		}
		if e.GitCommonDir != common {
			return id, false, fmt.Errorf("%w: %s is bound to git dir %s but is now at %s; rebind it explicitly", workflow.ErrConflict, root, e.GitCommonDir, common)
		}
		id.CheckoutID, id.StateRoot = e.ID, filepath.Join(realHome, e.ID)
		return id, true, nil
	}
	return id, false, nil
}

// stateHome is the base directory for all checkouts' state.
func stateHome(override string, configDir func() (string, error)) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	dir, err := configDir()
	if err != nil {
		return "", fmt.Errorf("finding the user config directory (set an explicit state home): %w", err)
	}
	return filepath.Join(dir, "tandem", "state"), nil
}

// realPath is p, absolute and with its symlinks resolved. EvalSymlinks keeps a
// relative path relative, and the git dir derived from it would be relative
// too, so every caller that compares paths goes through here.
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// gitIdentity returns the canonical worktree root and common git directory.
func gitIdentity(cwd string) (root, common string, err error) {
	real, err := realPath(cwd)
	if err != nil {
		return "", "", err
	}
	out, err := exec.Command("git", "-C", real, "rev-parse", "--show-toplevel", "--git-common-dir").Output()
	if err != nil {
		return "", "", fmt.Errorf("%s is not inside a git checkout; durable state is per git worktree: %w", cwd, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("unexpected git rev-parse output %q", out)
	}
	top, gc := lines[0], lines[1]
	if !filepath.IsAbs(gc) {
		gc = filepath.Join(real, gc) // relative to where git ran
	}
	if root, err = filepath.EvalSymlinks(top); err != nil {
		return "", "", err
	}
	if common, err = filepath.EvalSymlinks(gc); err != nil {
		return "", "", err
	}
	return root, common, nil
}

func manifestScope(cwd, root string) (string, error) {
	real, err := realPath(cwd)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(paths.NearestManifest(real, root))
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ".", nil
	}
	return filepath.ToSlash(rel), nil
}

// checkHome resolves the state home's real path, through symlinks even where
// the directory does not exist yet, and refuses one that deleting a relay or
// the checkout would take with it. Nothing is created.
func checkHome(home, checkoutRoot string) (string, error) {
	real := resolveThroughMissing(home)
	for dir := real; ; dir = filepath.Dir(dir) {
		if paths.IsRelayName(filepath.Base(dir)) {
			return "", fmt.Errorf("%w: %s is inside a relay directory, which tandem-unpair deletes", ErrUnsafeRoot, home)
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	if rel, err := filepath.Rel(checkoutRoot, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is inside the checkout %s", ErrUnsafeRoot, home, checkoutRoot)
	}
	return real, nil
}

// resolveThroughMissing resolves symlinks in the longest existing prefix of p
// and appends the rest unchanged.
func resolveThroughMissing(p string) string {
	var tail []string
	for cur := p; ; cur = filepath.Dir(cur) {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real
		}
		tail = append(tail, filepath.Base(cur))
		if filepath.Dir(cur) == cur {
			return p
		}
	}
}

// registryFile maps canonical worktrees to checkout ids.
type registryFile struct {
	Version   int             `json:"version"`
	Checkouts []registryEntry `json:"checkouts"`
}

type registryEntry struct {
	ID            string `json:"id"`
	CanonicalRoot string `json:"canonical_root"`
	GitCommonDir  string `json:"git_common_dir"`
	CreatedMS     int64  `json:"created_ms"`
}

const registryName = "registry.json"

// bind returns the checkout id for a worktree, creating it when new. It holds
// an exclusive lock for the read-decide-write, and writes by atomic rename.
// An ambiguous or inconsistent registry is workflow.ErrConflict: rebinding a
// moved worktree is an explicit operation, never a silent guess.
func bind(home, root, common string) (string, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	lock := &Locker{path: filepath.Join(home, "registry.lock")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unlock, err := lock.LockExclusive(ctx)
	if err != nil {
		return "", fmt.Errorf("locking the state registry: %w", err)
	}
	defer unlock()

	path := filepath.Join(home, registryName)
	var reg registryFile
	switch raw, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
		reg.Version = 1
	case err != nil:
		return "", err
	default:
		if err := json.Unmarshal(raw, &reg); err != nil {
			return "", fmt.Errorf("the state registry %s is corrupt (left untouched): %w", path, err)
		}
	}
	ids, roots := map[string]bool{}, map[string]int{}
	for i, e := range reg.Checkouts {
		if ids[e.ID] {
			return "", fmt.Errorf("%w: registry id %s is bound twice", workflow.ErrConflict, e.ID)
		}
		ids[e.ID] = true
		if _, dup := roots[e.CanonicalRoot]; dup {
			return "", fmt.Errorf("%w: registry binds %s twice", workflow.ErrConflict, e.CanonicalRoot)
		}
		roots[e.CanonicalRoot] = i
	}
	if i, ok := roots[root]; ok {
		if got := reg.Checkouts[i].GitCommonDir; got != common {
			return "", fmt.Errorf("%w: %s is bound to git dir %s but is now at %s; rebind it explicitly", workflow.ErrConflict, root, got, common)
		}
		return reg.Checkouts[i].ID, nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	if ids[id] {
		return "", fmt.Errorf("%w: random checkout id collided", workflow.ErrConflict)
	}
	reg.Version = 1
	reg.Checkouts = append(reg.Checkouts, registryEntry{ID: id, CanonicalRoot: root, GitCommonDir: common, CreatedMS: time.Now().UnixMilli()})
	return id, writeRegistry(home, path, reg)
}

func writeRegistry(home, path string, reg registryFile) error {
	body, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, registryName+".*")
	if err != nil {
		return err
	}
	cleanup := func(err error) error { tmp.Close(); os.Remove(tmp.Name()); return err }
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return cleanup(err)
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	d, err := os.Open(home)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
