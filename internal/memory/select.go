package memory

import (
	"fmt"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Item is one entry's place in an Explanation.
type Item struct {
	ID       string
	Reason   string
	Required bool
}

// Conflict is a set of required entries that cannot all hold.
type Conflict struct {
	Kind    string
	Scope   []string
	IDs     []string
	Sources []string
}

// Explanation is why each entry was or was not selected for a task.
type Explanation struct {
	// Included are in selection order: direct dependencies first, then path
	// scope, then by id.
	Included []Item
	Excluded []Item
	// Conflicts are required entries that contradict each other. Their members are
	// in neither list.
	Conflicts []Conflict
	// StaleRequired are required, applicable entries that went invalid or stale:
	// something mandatory is no longer available, and a caller must not render
	// the task as if it were.
	StaleRequired []string
}

// Explain classifies every entry for task. valid holds the validity hashes that
// are current. It does not change its input.
func Explain(entries []Entry, task workflow.TaskSpec, valid map[string]bool) Explanation {
	direct := map[string]bool{task.ID: true}
	for _, d := range task.DependsOn {
		direct[d] = true
	}
	replaced := map[string]bool{}
	for _, e := range entries {
		if e.SupersedesID != "" && (e.State == StateActive || e.State == "") {
			replaced[e.SupersedesID] = true
		}
	}

	type cand struct {
		e     Entry
		group int
	}
	var cands []cand
	var x Explanation
	exclude := func(e Entry, reason string) {
		x.Excluded = append(x.Excluded, Item{ID: e.ID, Reason: reason, Required: e.Required})
		if e.Required && (reason == ReasonStale || reason == ReasonInvalid) {
			x.StaleRequired = append(x.StaleRequired, e.ID)
		}
	}
	for _, e := range entries {
		group := -1
		switch {
		case direct[e.TaskID] || anyIn(e.Dependencies, direct):
			group = 0
		case ScopeOverlaps(e.Scope, task.Paths):
			group = 1
		}
		switch {
		case group < 0:
			exclude(e, ReasonNotApplicable)
		case e.State == StateInvalid:
			exclude(e, ReasonInvalid)
		case e.State == StateSuperseded || replaced[e.ID]:
			exclude(e, ReasonSuperseded)
		case e.State == StateConflicted:
			exclude(e, ReasonConflicted)
		case !valid[e.ValidityHash]:
			exclude(e, ReasonStale)
		default:
			cands = append(cands, cand{e, group})
		}
	}

	// Required decisions and contracts about the same thing must agree.
	type key struct{ kind, scope string }
	groups := map[key][]Entry{}
	for _, c := range cands {
		if c.e.Required && (c.e.Kind == KindApprovedDecision || c.e.Kind == KindInterfaceContract) && len(c.e.Scope) > 0 {
			sc, _ := NormalizeScope(c.e.Scope)
			k := key{c.e.Kind, strings.Join(sc, "\x00")}
			groups[k] = append(groups[k], c.e)
		}
	}
	conflicting := map[string]bool{}
	var keys []key
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].scope < keys[j].scope
	})
	for _, k := range keys {
		g := groups[k]
		values := map[string]bool{}
		for _, e := range g {
			values[e.ValueHash] = true
		}
		if len(values) < 2 {
			continue
		}
		sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
		sc, _ := NormalizeScope(g[0].Scope)
		cf := Conflict{Kind: k.kind, Scope: sc}
		for _, e := range g {
			cf.IDs = append(cf.IDs, e.ID)
			cf.Sources = append(cf.Sources, e.SourceHash)
			conflicting[e.ID] = true
		}
		x.Conflicts = append(x.Conflicts, cf)
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].group != cands[j].group {
			return cands[i].group < cands[j].group
		}
		return cands[i].e.ID < cands[j].e.ID
	})
	for _, c := range cands {
		if conflicting[c.e.ID] {
			continue
		}
		reason := ReasonDependency
		if c.group == 1 {
			reason = ReasonPathScope
		}
		x.Included = append(x.Included, Item{ID: c.e.ID, Reason: reason, Required: c.e.Required})
	}
	sort.Slice(x.Excluded, func(i, j int) bool { return x.Excluded[i].ID < x.Excluded[j].ID })
	sort.Strings(x.StaleRequired)
	return x
}

func anyIn(list []string, set map[string]bool) bool {
	for _, s := range list {
		if set[s] {
			return true
		}
	}
	return false
}

// Select returns the entries task may be shown, in order. Required entries that
// contradict each other are an error naming every id and source, so the task is
// not rendered from a guess.
func Select(entries []Entry, task workflow.TaskSpec, valid map[string]bool) ([]Entry, error) {
	x := Explain(entries, task, valid)
	if len(x.Conflicts) > 0 {
		var parts []string
		for _, c := range x.Conflicts {
			parts = append(parts, fmt.Sprintf("required %s entries %s disagree about %s (sources %s)",
				c.Kind, strings.Join(c.IDs, ", "), strings.Join(c.Scope, ", "), strings.Join(c.Sources, ", ")))
		}
		return nil, fmt.Errorf("%w: %s", workflow.ErrMemoryConflict, strings.Join(parts, "; "))
	}
	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	out := make([]Entry, 0, len(x.Included))
	for _, it := range x.Included {
		out = append(out, byID[it.ID])
	}
	return out, nil
}
