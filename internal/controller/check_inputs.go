package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/snapshot"
)

func nonNil(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

// resolve validates the request and returns the canonical root, the directory
// to run in, and that directory relative to the root.
func (r CheckRequest) resolve() (root, cwd, rel string, err error) {
	switch {
	case !idPattern.MatchString(r.JobID) || !idPattern.MatchString(r.AttemptID):
		return "", "", "", errors.New("job and attempt ids must be 32 lowercase hex characters")
	case strings.TrimSpace(r.Command) == "":
		return "", "", "", errors.New("a check needs a command")
	}
	if root, err = filepath.EvalSymlinks(r.Root); err != nil {
		return "", "", "", fmt.Errorf("check root: %w", err)
	}
	cwd, rel = root, "."
	if r.Cwd != "" && r.Cwd != "." {
		clean := filepath.Clean(r.Cwd)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", "", "", fmt.Errorf("check cwd %q must stay inside the checkout", r.Cwd)
		}
		real, err := filepath.EvalSymlinks(filepath.Join(root, clean))
		if err != nil {
			return "", "", "", fmt.Errorf("check cwd: %w", err)
		}
		if in, err := filepath.Rel(root, real); err != nil || in == ".." || strings.HasPrefix(in, ".."+string(filepath.Separator)) {
			return "", "", "", fmt.Errorf("check cwd %q resolves outside the checkout", r.Cwd)
		}
		cwd, rel = real, filepath.ToSlash(clean)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return "", "", "", fmt.Errorf("check cwd %q is not a directory", r.Cwd)
	}
	return root, cwd, rel, nil
}

type resolvedEnv struct {
	env   []envEntry
	tools []toolEntry
}

// environment fingerprints the declared variables (by value hash) and tools.
func environment(in CheckInputs) (resolvedEnv, error) {
	var out resolvedEnv
	names := append([]string(nil), in.Env...)
	sort.Strings(names)
	for _, n := range names {
		if !envPattern.MatchString(n) {
			return out, fmt.Errorf("%q is not an environment variable name", n)
		}
		h := "unset"
		if v, ok := os.LookupEnv(n); ok {
			sum := sha256.Sum256([]byte(v))
			h = hex.EncodeToString(sum[:])
		}
		out.env = append(out.env, envEntry{Name: n, ValueHash: h})
	}
	if out.env == nil {
		out.env = []envEntry{}
	}
	tools, err := hashTools(in.Tools)
	out.tools = tools
	return out, err
}

func hashTools(names []string) ([]toolEntry, error) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	out := []toolEntry{}
	for _, name := range sorted {
		path := name
		if !strings.Contains(name, "/") {
			p, err := exec.LookPath(name)
			if err != nil {
				return nil, fmt.Errorf("declared tool %q: %w", name, err)
			}
			path = p
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("declared tool %q: %w", name, err)
		}
		h, err := hashFile(real)
		if err != nil {
			return nil, fmt.Errorf("declared tool %q: %w", name, err)
		}
		out = append(out, toolEntry{Name: name, Path: real, Hash: h})
	}
	return out, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fingerprint hashes everything the check's result depends on: the snapshot of
// the checkout (tracked and untracked content, mode, kind), the command and
// directory, the declared environment and tools, and the declared files.
func fingerprint(ctx context.Context, art *artifact.Store, root string, in CheckInputs, cmdHash, envHash string) (hash string, complete bool, errs []string, err error) {
	m, err := snapshot.Capture(ctx, root, art)
	if err != nil {
		return "", false, nil, fmt.Errorf("capturing the check's inputs: %w", err)
	}
	doc := inputsDoc{Version: 1, SnapshotHash: m.Hash, SnapshotComplete: m.Complete, CommandHash: cmdHash,
		EnvironmentHash: envHash, Files: []fileEntry{}}
	errs = append(errs, m.Errors...)
	files := append([]string(nil), in.Files...)
	sort.Strings(files)
	for _, f := range files {
		h, err := declaredFile(root, f)
		if err != nil {
			return "", false, nil, err
		}
		doc.Files = append(doc.Files, fileEntry{Path: filepath.ToSlash(filepath.Clean(f)), Hash: h})
	}
	if doc.Tools, err = hashTools(in.Tools); err != nil {
		return "", false, nil, err
	}
	hash, err = putDoc(ctx, art, doc)
	return hash, m.Complete, errs, err
}

// declaredFile hashes one declared input file: absent is a state, not an error;
// a symlink contributes its target text, never the content it points at.
func declaredFile(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("declared input %q must be a path inside the checkout", rel)
	}
	path := filepath.Join(root, clean)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "absent", nil
	case err != nil:
		return "", err
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte("symlink:" + target))
		return hex.EncodeToString(sum[:]), nil
	}
	return hashFile(path)
}
