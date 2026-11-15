package pairname

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkout is what Derive and Resolve need to know about the directory.
type checkout struct {
	real   string // dir, absolute with symlinks resolved
	top    string // git toplevel, or real when dir is not inside git
	inGit  bool
	isMain bool
	others []string // slugs of the other linked worktrees' folders
}

func inspect(dir string) (checkout, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return checkout{}, err
	}
	if real, err = filepath.Abs(real); err != nil {
		return checkout{}, err
	}
	out, err := exec.Command("git", "-C", real, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir").Output()
	if err != nil {
		return checkout{real: real, top: real}, nil // not inside git
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		return checkout{}, fmt.Errorf("unexpected git rev-parse output %q", out)
	}
	top, err := filepath.EvalSymlinks(lines[0])
	if err != nil {
		return checkout{}, err
	}
	gitDir, err := resolveFrom(real, lines[1])
	if err != nil {
		return checkout{}, err
	}
	common, err := resolveFrom(real, lines[2])
	if err != nil {
		return checkout{}, err
	}
	c := checkout{real: real, top: top, inGit: true, isMain: gitDir == common}
	if c.isMain {
		return c, nil
	}
	list, err := exec.Command("git", "-C", real, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return checkout{}, fmt.Errorf("listing worktrees: %w", err)
	}
	c.others = linkedSlugs(string(list), top)
	return c, nil
}

// linkedSlugs returns the folder slugs of the linked worktrees in list, the
// output of `git worktree list --porcelain`, other than top. It skips the main
// worktree, which git lists first and which always uses the plain names. It
// also skips a bare entry and a prunable one, whose folder is gone, because
// neither can hold a pair.
func linkedSlugs(list, top string) []string {
	var slugs []string
	for i, block := range strings.Split(strings.TrimSpace(list), "\n\n") {
		if i == 0 {
			continue
		}
		p, skip := "", false
		for _, l := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(l, "worktree "); ok {
				p = v
			}
			if l == "bare" || l == "prunable" || strings.HasPrefix(l, "prunable ") {
				skip = true
			}
		}
		if skip || p == "" {
			continue
		}
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			p = rp
		}
		if p != top {
			slugs = append(slugs, Slug(filepath.Base(p)))
		}
	}
	return slugs
}

// Checkout returns the checkout holding dir: its git toplevel with symlinks
// resolved, or dir itself, resolved, outside git. Two directories are in the
// same checkout when their Checkout values are equal.
func Checkout(dir string) (string, error) {
	c, err := inspect(dir)
	if err != nil {
		return "", err
	}
	return c.top, nil
}

// resolveFrom makes a path printed by git absolute against base and resolves
// its symlinks.
func resolveFrom(base, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.EvalSymlinks(p)
}
