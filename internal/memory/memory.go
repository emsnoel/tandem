// Package memory decides which remembered facts a task may be shown, and why.
//
// A fact is a scoped entry with provenance: where it came from, how far it can
// be trusted, what it is bound to and what replaced it. Selection is by
// applicability and validity, never by recency: a newer entry does not displace
// an older one unless it names it and is at least as trusted. Builder prose is a
// claim, and a claim can neither be required nor take the place of anything
// stronger.
//
// The package is pure. It does no I/O and holds no state: the controller loads
// entries, with their bounded values, and says which validity hashes are current.
package memory

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Kinds of entry.
const (
	KindApprovedDecision  = "approved_decision"
	KindInterfaceContract = "interface_contract"
	KindCheckOutcome      = "check_outcome"
	KindUnresolvedIssue   = "unresolved_issue"
	KindBuilderClaim      = "builder_claim"
)

// Trust levels, weakest first.
const (
	TrustClaim    = "claim"
	TrustObserved = "observed"
	TrustApproved = "approved"
)

// Entry states.
const (
	StateActive     = "active"
	StateSuperseded = "superseded"
	StateInvalid    = "invalid"
	StateConflicted = "conflicted"
)

// Why an entry was included or left out.
const (
	ReasonDependency    = "direct_dependency"
	ReasonPathScope     = "path_scope"
	ReasonNotApplicable = "not_applicable"
	ReasonSuperseded    = "superseded"
	ReasonInvalid       = "invalid"
	ReasonStale         = "stale_validity"
	ReasonConflicted    = "conflicted"
)

// Entry is one remembered fact. The store keeps its hashes and relations; Value
// and Scope are loaded by the controller from the artifacts those hashes name,
// bounded, before an entry reaches Select.
type Entry struct {
	ID, RunID string
	// TaskID is the task that produced the entry.
	TaskID string
	Kind   string
	// ScopeHash names the canonical scope artifact. Two entries have the same
	// scope exactly when their scope hashes are equal.
	ScopeHash, SourceHash string
	// ValidityHash binds the entry to the content it was derived from. It is
	// current only while the controller lists it among the valid hashes.
	ValidityHash string
	// SupersedesID names the entry this one replaces, explicitly.
	SupersedesID string
	Trust, State string
	ValueHash    string
	Value        string
	// Scope is the path prefixes the entry is about.
	Scope []string
	// Dependencies are the tasks whose accepted content the entry was derived from.
	Dependencies []string
	// Required entries must reach the builder; the packet never trims them.
	Required bool
}

var (
	idPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// allowedTrust is the trust each kind may be recorded with. Evidence sets the
// ceiling: a check can observe, only a planner decision approves, and prose
// from a builder stays a claim.
var allowedTrust = map[string][]string{
	KindApprovedDecision:  {TrustApproved},
	KindInterfaceContract: {TrustObserved, TrustApproved},
	KindCheckOutcome:      {TrustObserved},
	KindUnresolvedIssue:   {TrustClaim, TrustObserved, TrustApproved},
	KindBuilderClaim:      {TrustClaim},
}

// Rank orders trust levels; an unknown level ranks below every known one.
func Rank(trust string) int {
	switch trust {
	case TrustClaim:
		return 1
	case TrustObserved:
		return 2
	case TrustApproved:
		return 3
	}
	return 0
}

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: memory entry: %s", workflow.ErrInvalidTransition, fmt.Sprintf(format, a...))
}

// ValidateNew checks an entry that is about to be recorded. It checks the entry
// alone; whether it may supersede another is CanSupersede's question.
func ValidateNew(e Entry) error {
	switch {
	case !idPattern.MatchString(e.ID):
		return bad("id must be 32 lowercase hex characters")
	case !idPattern.MatchString(e.RunID):
		return bad("run id must be 32 lowercase hex characters")
	case !workflow.ValidTaskID(e.TaskID):
		return bad("task %q is not a task id", e.TaskID)
	}
	trusts, ok := allowedTrust[e.Kind]
	if !ok {
		return bad("kind %q is not one of the five memory kinds", e.Kind)
	}
	if Rank(e.Trust) == 0 {
		return bad("trust %q is not claim, observed or approved", e.Trust)
	}
	allowed := false
	for _, t := range trusts {
		allowed = allowed || t == e.Trust
	}
	if !allowed {
		return bad("a %s entry cannot carry %s trust (allowed: %s)", e.Kind, e.Trust, strings.Join(trusts, ", "))
	}
	if e.State != "" && e.State != StateActive {
		return bad("a new entry's state is active, not %s", e.State)
	}
	if e.Required && e.Trust == TrustClaim {
		return bad("a claim cannot be required: nothing has established it")
	}
	for name, h := range map[string]string{"source": e.SourceHash, "validity": e.ValidityHash, "value": e.ValueHash, "scope": e.ScopeHash} {
		if !hashPattern.MatchString(h) {
			return bad("%s hash must be 64 lowercase hex characters", name)
		}
	}
	if e.SupersedesID != "" {
		if !idPattern.MatchString(e.SupersedesID) {
			return bad("supersedes id must be 32 lowercase hex characters")
		}
		if e.SupersedesID == e.ID {
			return bad("an entry cannot supersede itself")
		}
	}
	if _, err := NormalizeScope(e.Scope); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, d := range e.Dependencies {
		if !workflow.ValidTaskID(d) {
			return bad("dependency %q is not a task id", d)
		}
		if seen[d] {
			return bad("dependency %q listed twice", d)
		}
		seen[d] = true
	}
	return nil
}

// CanSupersede says whether next may take the place of old: it names it, old is
// still active, and the two have the same kind and scope, with next at least as
// trusted. Newer is not more trustworthy, so age plays no part.
func CanSupersede(old, next Entry) error {
	switch {
	case next.SupersedesID != old.ID:
		return bad("%s does not name %s as the entry it supersedes", next.ID, old.ID)
	case old.State != StateActive:
		return bad("%s is %s and cannot be superseded", old.ID, old.State)
	case next.Kind != old.Kind:
		return bad("%s is a %s and %s a %s: supersession needs the same kind", next.ID, next.Kind, old.ID, old.Kind)
	case next.ScopeHash != old.ScopeHash:
		return bad("%s and %s have different scopes: supersession needs the same scope", next.ID, old.ID)
	case Rank(next.Trust) < Rank(old.Trust):
		return bad("a %s entry (%s) cannot supersede a %s one (%s): trust would drop", next.Trust, next.ID, old.Trust, old.ID)
	}
	return nil
}

// NormalizeScope cleans a scope into sorted, unique relative path prefixes.
func NormalizeScope(scope []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range scope {
		if err := workflow.ValidRelPath(p); err != nil {
			return nil, bad("scope path %q %v", p, err)
		}
		c := path.Clean(p)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ScopeOverlaps reports whether any prefix of a contains, or is contained in,
// any prefix of b, on path boundaries: internal/state overlaps internal and
// internal/state/x but not internal/statement.
func ScopeOverlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if prefixOf(path.Clean(x), path.Clean(y)) || prefixOf(path.Clean(y), path.Clean(x)) {
				return true
			}
		}
	}
	return false
}

func prefixOf(p, of string) bool {
	return p == "." || p == of || strings.HasPrefix(of, p+"/")
}
