// Package pairname derives the Herdr agent names for a pair. Herdr names are
// global, so two worktrees of one repository need different names.
package pairname

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
// slug is empty or clashes with another linked worktree's slug.
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

// PendingID stands in for the checkout id in names that ResolveReadOnly cannot
// complete, because the checkout has no id yet.
const PendingID = "<id>"

var errUnbound = errors.New("checkout has no id yet")

func (c checkout) derive(dir, stateHome string, readOnly bool) (Names, error) {
	if !c.inGit {
		return plain, nil
	}
	return derive(c.isMain, Slug(filepath.Base(c.top)), c.others, func() (string, error) {
		if !readOnly {
			id, err := state.ResolveIdentity(dir, stateHome)
			return id.CheckoutID, err
		}
		id, bound, err := state.LookupIdentity(dir, stateHome)
		if err != nil {
			return "", err
		}
		if !bound {
			return "", errUnbound
		}
		return id.CheckoutID, nil
	})
}

// Derive returns the names for the checkout containing dir. Outside git and in
// a repository's main checkout the names are the plain ones. Only a linked
// worktree whose folder name is unusable or clashes with another linked
// worktree's consults the state registry.
func Derive(dir, stateHome string) (Names, error) {
	c, err := inspect(dir)
	if err != nil {
		return Names{}, err
	}
	return c.derive(dir, stateHome, false)
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
	n, _, err := resolve(dir, stateHome, getenv, false)
	return n, err
}

// ResolveReadOnly is Resolve without any write. A linked worktree that needs
// the registry's checkout id but has none yet gets PendingID in place of the id,
// and pending is true. It never creates the state home, the registry or a binding.
func ResolveReadOnly(dir, stateHome string, getenv func(string) string) (names Names, pending bool, err error) {
	return resolve(dir, stateHome, getenv, true)
}

func resolve(dir, stateHome string, getenv func(string) string, readOnly bool) (Names, bool, error) {
	env := Names{
		Planner:  getenv("HERDR_PLANNER_NAME"),
		Builder:  getenv("HERDR_BUILDER_NAME"),
		Reviewer: getenv("HERDR_REVIEWER_NAME"),
	}
	for _, o := range []struct{ name, val string }{
		{"HERDR_PLANNER_NAME", env.Planner}, {"HERDR_BUILDER_NAME", env.Builder}, {"HERDR_REVIEWER_NAME", env.Reviewer},
	} {
		if o.val != "" && !nameRE.MatchString(o.val) {
			return Names{}, false, fmt.Errorf("%s='%s' is not a Herdr agent name; it must match %s", o.name, o.val, nameRE.String())
		}
	}
	c, err := inspect(dir)
	if err != nil {
		return Names{}, false, err
	}
	derived, err := c.derive(dir, stateHome, readOnly)
	pending := errors.Is(err, errUnbound)
	if pending {
		derived, err = suffixed(PendingID), nil
	}
	if err != nil {
		return Names{}, false, err
	}
	// The relay lives at the checkout's toplevel, wherever dir is inside it.
	rec, _, err := ReadRecord(paths.Relay(c.top))
	if err != nil {
		return Names{}, false, err
	}
	n := merge(env, rec, derived)
	// Overrides and the record may cover every role that would have used the id.
	pending = pending && (n.Planner == derived.Planner || n.Builder == derived.Builder || n.Reviewer == derived.Reviewer)
	return n, pending, nil
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
