// Package pairname derives the Herdr agent names for a pair. Herdr names are
// global, so two worktrees of one repository need different names.
package pairname

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
)

// Names are the Herdr agent names of one pair.
type Names struct {
	Planner  string `json:"planner"`
	Builder  string `json:"builder"`
	Reviewer string `json:"reviewer"`
}

// RecordName is the file in the relay directory that records the names a pair
// was started with.
const RecordName = "pair.json"

// maxSlug is Herdr's 32 character name limit minus len("reviewer-").
const maxSlug = 23

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

var plain = Names{Planner: "planner", Builder: "builder", Reviewer: "reviewer"}

// Slug turns a folder name into a suffix that keeps every pair name within
// Herdr's limits. It may return "".
func Slug(folder string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(folder) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := collapse(b.String())
	s = strings.Trim(s, "-")
	if len(s) > maxSlug {
		s = strings.TrimRight(s[:maxSlug], "-")
	}
	return s
}

func collapse(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func suffixed(suffix string) Names {
	return Names{"planner-" + suffix, "builder-" + suffix, "reviewer-" + suffix}
}

// derive is the pure naming rule. checkoutID is called only when the folder
// slug is empty or clashes with another worktree's slug.
func derive(isMain bool, slug string, otherSlugs []string, checkoutID func() (string, error)) (Names, error) {
	if isMain {
		return plain, nil
	}
	clash := slug == ""
	for _, o := range otherSlugs {
		if o == slug {
			clash = true
		}
	}
	if !clash {
		return suffixed(slug), nil
	}
	id, err := checkoutID()
	if err != nil {
		return Names{}, err
	}
	if len(id) < 6 {
		return Names{}, fmt.Errorf("checkout id %q is too short", id)
	}
	return suffixed(id[:6]), nil
}

// checkout is what Derive and Resolve need to know about the directory.
type checkout struct {
	real   string // dir, absolute with symlinks resolved
	top    string // git toplevel, or real when dir is not inside git
	inGit  bool
	isMain bool
	others []string // slugs of the other worktrees' folders
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
	for _, l := range strings.Split(string(list), "\n") {
		p, ok := strings.CutPrefix(l, "worktree ")
		if !ok {
			continue
		}
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			p = rp
		}
		if p != top {
			c.others = append(c.others, Slug(filepath.Base(p)))
		}
	}
	return c, nil
}

func (c checkout) derive(dir, stateHome string) (Names, error) {
	if !c.inGit {
		return plain, nil
	}
	return derive(c.isMain, Slug(filepath.Base(c.top)), c.others, func() (string, error) {
		id, err := state.ResolveIdentity(dir, stateHome)
		return id.CheckoutID, err
	})
}

// Derive returns the names for the checkout containing dir. Outside git and in
// a repository's main checkout the names are the plain ones. Only a linked
// worktree whose folder name is unusable or clashes consults the state registry.
func Derive(dir, stateHome string) (Names, error) {
	c, err := inspect(dir)
	if err != nil {
		return Names{}, err
	}
	return c.derive(dir, stateHome)
}

// resolveFrom makes a path printed by git absolute against base and resolves
// its symlinks.
func resolveFrom(base, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.EvalSymlinks(p)
}

// ReadRecord reads <relay>/pair.json. A missing file is not an error. A name
// that fails Herdr's pattern is dropped, so a hand-edited bad name falls
// through to the derived one.
func ReadRecord(relay string) (Names, bool, error) {
	raw, err := os.ReadFile(filepath.Join(relay, RecordName))
	if errors.Is(err, fs.ErrNotExist) {
		return Names{}, false, nil
	}
	if err != nil {
		return Names{}, false, err
	}
	var n Names
	if err := json.Unmarshal(raw, &n); err != nil {
		return Names{}, false, fmt.Errorf("%s is not valid JSON (%v); remove it and run tandem-pair again", filepath.Join(relay, RecordName), err)
	}
	for _, f := range []*string{&n.Planner, &n.Builder, &n.Reviewer} {
		if !nameRE.MatchString(*f) {
			*f = ""
		}
	}
	return n, true, nil
}

// Resolve returns the names to use for dir. Per role, an environment override
// wins over the recorded name, which wins over the derived one. An override
// that is not a Herdr name is an error, because tandem-pair writes the names
// into pair.json unescaped.
func Resolve(dir, stateHome string, getenv func(string) string) (Names, error) {
	env := Names{
		Planner:  getenv("HERDR_PLANNER_NAME"),
		Builder:  getenv("HERDR_BUILDER_NAME"),
		Reviewer: getenv("HERDR_REVIEWER_NAME"),
	}
	for _, o := range []struct{ name, val string }{
		{"HERDR_PLANNER_NAME", env.Planner}, {"HERDR_BUILDER_NAME", env.Builder}, {"HERDR_REVIEWER_NAME", env.Reviewer},
	} {
		if o.val != "" && !nameRE.MatchString(o.val) {
			return Names{}, fmt.Errorf("%s='%s' is not a Herdr agent name; it must match %s", o.name, o.val, nameRE.String())
		}
	}
	c, err := inspect(dir)
	if err != nil {
		return Names{}, err
	}
	derived, err := c.derive(dir, stateHome)
	if err != nil {
		return Names{}, err
	}
	// The relay lives at the checkout's toplevel, wherever dir is inside it.
	rec, _, err := ReadRecord(paths.Relay(c.top))
	if err != nil {
		return Names{}, err
	}
	return merge(env, rec, derived), nil
}

// merge picks, per role, the first non-empty of env, rec and derived.
func merge(env, rec, derived Names) Names {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}
	return Names{
		Planner:  pick(env.Planner, rec.Planner, derived.Planner),
		Builder:  pick(env.Builder, rec.Builder, derived.Builder),
		Reviewer: pick(env.Reviewer, rec.Reviewer, derived.Reviewer),
	}
}
