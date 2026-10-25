package memory

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func hex32(n int) string { return fmt.Sprintf("%032x", n) }
func hex64(n int) string { return fmt.Sprintf("%064x", n) }

// entry builds an applicable, valid, active entry produced by task "dep" about
// internal/state. Tests change what they are about.
func entry(n int, mut ...func(*Entry)) Entry {
	e := Entry{ID: hex32(n), RunID: hex32(1), TaskID: "dep", Kind: KindApprovedDecision, Trust: TrustApproved, State: StateActive,
		Scope: []string{"internal/state"}, ScopeHash: hex64(1000), SourceHash: hex64(2000 + n), ValidityHash: hex64(3000), ValueHash: hex64(4000 + n),
		Value: fmt.Sprintf("decision %d", n)}
	for _, m := range mut {
		m(&e)
	}
	return e
}

func ids(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID[len(e.ID)-2:])
	}
	return strings.Join(out, ",")
}

var consumer = workflow.TaskSpec{ID: "consumer", DependsOn: []string{"dep"}, Paths: []string{"internal/state"}}

func valid() map[string]bool { return map[string]bool{hex64(3000): true} }

func TestSelectOrdersByDependencyThenPathScopeThenID(t *testing.T) {
	task := workflow.TaskSpec{ID: "consumer", DependsOn: []string{"dep"}, Paths: []string{"internal/state"}}
	entries := []Entry{
		entry(9, func(e *Entry) { e.TaskID = "other"; e.Scope = []string{"internal/state/sub"} }), // path scope only
		entry(5, func(e *Entry) { e.TaskID = "other"; e.Scope = []string{"internal"} }),           // path scope only, lower id
		entry(7), // direct dependency
		entry(3, func(e *Entry) { e.TaskID = "consumer"; e.Scope = nil }),                                    // the task's own history
		entry(2, func(e *Entry) { e.TaskID = "unrelated"; e.Scope = []string{"cmd/tandem"} }),                // neither
		entry(4, func(e *Entry) { e.TaskID = "unrelated"; e.Scope = nil; e.Dependencies = []string{"dep"} }), // derived from a dependency
	}
	got, err := Select(entries, task, valid())
	if err != nil {
		t.Fatal(err)
	}
	if want := "03,04,07,05,09"; ids(got) != want {
		t.Fatalf("selected %s, want %s", ids(got), want)
	}
}

func TestUnrelatedFactsAreExcludedWithAReason(t *testing.T) {
	entries := []Entry{entry(1), entry(2, func(e *Entry) { e.TaskID = "far"; e.Scope = []string{"cmd/other"} })}
	x := Explain(entries, consumer, valid())
	if len(x.Included) != 1 || x.Included[0].ID != hex32(1) || x.Included[0].Reason != ReasonDependency {
		t.Fatalf("included %+v", x.Included)
	}
	if len(x.Excluded) != 1 || x.Excluded[0].ID != hex32(2) || x.Excluded[0].Reason != ReasonNotApplicable {
		t.Fatalf("excluded %+v", x.Excluded)
	}
}

func TestPathScopeIsAPrefixOnPathBoundaries(t *testing.T) {
	for _, c := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"internal/state"}, []string{"internal/state"}, true},
		{[]string{"internal"}, []string{"internal/state"}, true},
		{[]string{"internal/state/x"}, []string{"internal/state"}, true},
		{[]string{"internal/state"}, []string{"internal/statement"}, false},
		{[]string{"internal/state"}, []string{"cmd"}, false},
		{nil, []string{"internal"}, false},
		{[]string{"internal"}, nil, false},
		{[]string{"a", "internal/state"}, []string{"b", "internal"}, true},
		{[]string{"internal/state/"}, []string{"internal/state"}, true},
		{[]string{"./internal"}, []string{"internal/state"}, true},
	} {
		if got := ScopeOverlaps(c.a, c.b); got != c.want {
			t.Errorf("%v vs %v: %v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestStaleSupersededAndInvalidEntriesAreExcludedWithReasons(t *testing.T) {
	entries := []Entry{
		entry(1, func(e *Entry) { e.ValidityHash = hex64(9999) }), // dependency content changed
		entry(2, func(e *Entry) { e.State = StateInvalid }),
		entry(3, func(e *Entry) { e.State = StateSuperseded }),
		entry(4),
		entry(5, func(e *Entry) { e.SupersedesID = hex32(4); e.Value = "newer" }), // supersedes 4 but 4 not yet marked
		entry(6, func(e *Entry) { e.State = StateConflicted }),
	}
	x := Explain(entries, consumer, valid())
	reasons := map[string]string{}
	for _, it := range x.Excluded {
		reasons[it.ID[len(it.ID)-2:]] = it.Reason
	}
	want := map[string]string{"01": ReasonStale, "02": ReasonInvalid, "03": ReasonSuperseded, "04": ReasonSuperseded, "06": ReasonConflicted}
	for id, r := range want {
		if reasons[id] != r {
			t.Errorf("entry %s excluded as %q, want %q", id, reasons[id], r)
		}
	}
	if len(x.Included) != 1 || x.Included[0].ID != hex32(5) {
		t.Errorf("included %+v", x.Included)
	}
}

func TestMemoryDependencyChangeInvalidates(t *testing.T) {
	e := entry(1, func(e *Entry) { e.Required = true; e.Kind = KindInterfaceContract; e.Trust = TrustObserved })
	if got, err := Select([]Entry{e}, consumer, valid()); err != nil || len(got) != 1 {
		t.Fatalf("before the change: %v %v", got, err)
	}
	// The dependency's content changed: its binding hash is no longer current.
	changed := map[string]bool{hex64(3001): true}
	got, err := Select([]Entry{e}, consumer, changed)
	if err != nil || len(got) != 0 {
		t.Fatalf("after the change: %v %v", got, err)
	}
	x := Explain([]Entry{e}, consumer, changed)
	if len(x.Excluded) != 1 || x.Excluded[0].Reason != ReasonStale || !x.Excluded[0].Required {
		t.Errorf("a required entry that went stale must say it was required: %+v", x.Excluded)
	}
	if len(x.StaleRequired) != 1 || x.StaleRequired[0] != e.ID {
		t.Errorf("stale required %v", x.StaleRequired)
	}
}

func TestMemoryRequiredConflictNamesSources(t *testing.T) {
	a := entry(1, func(e *Entry) { e.Required = true })
	b := entry(2, func(e *Entry) { e.Required = true; e.TaskID = "other-dep" })
	task := workflow.TaskSpec{ID: "consumer", DependsOn: []string{"dep", "other-dep"}}
	got, err := Select([]Entry{a, b}, task, valid())
	if !errors.Is(err, workflow.ErrMemoryConflict) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	for _, want := range []string{a.ID, b.ID, a.SourceHash, b.SourceHash} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %s: %v", want, err)
		}
	}
	x := Explain([]Entry{a, b}, task, valid())
	if len(x.Conflicts) != 1 || strings.Join(x.Conflicts[0].IDs, ",") != a.ID+","+b.ID || len(x.Conflicts[0].Sources) != 2 {
		t.Errorf("conflicts %+v", x.Conflicts)
	}
	if len(x.Included) != 0 {
		t.Errorf("conflicting entries were still included: %+v", x.Included)
	}
}

func TestNonConflictsAreNotConflicts(t *testing.T) {
	task := workflow.TaskSpec{ID: "consumer", DependsOn: []string{"dep", "other"}}
	for name, c := range map[string]struct{ a, b func(*Entry) }{
		"same value":      {func(e *Entry) { e.Required = true }, func(e *Entry) { e.Required = true; e.ValueHash = hex64(4001) }},
		"one is optional": {func(e *Entry) { e.Required = true }, func(e *Entry) { e.TaskID = "other" }},
		"different scope": {func(e *Entry) { e.Required = true }, func(e *Entry) { e.Required = true; e.TaskID = "other"; e.Scope = []string{"cmd"} }},
		"different kind": {func(e *Entry) { e.Required = true }, func(e *Entry) {
			e.Required = true
			e.TaskID = "other"
			e.Kind = KindInterfaceContract
			e.Trust = TrustObserved
		}},
		"no scope to compare": {func(e *Entry) { e.Required = true; e.Scope = nil }, func(e *Entry) { e.Required = true; e.TaskID = "other"; e.Scope = nil }},
		"one superseded":      {func(e *Entry) { e.Required = true; e.State = StateSuperseded }, func(e *Entry) { e.Required = true; e.TaskID = "other" }},
	} {
		a, b := entry(1, c.a), entry(2, c.b)
		if name == "same value" {
			b.ValueHash = a.ValueHash
		}
		if _, err := Select([]Entry{a, b}, task, valid()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestClaimsAreLabelledAndNeverRequired(t *testing.T) {
	claim := entry(1, func(e *Entry) { e.Kind = KindBuilderClaim; e.Trust = TrustClaim })
	if err := ValidateNew(claim); err != nil {
		t.Fatal(err)
	}
	claim.Required = true
	if err := ValidateNew(claim); err == nil || !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("a claim cannot be required: %v", err)
	}
	up := entry(2, func(e *Entry) { e.Kind = KindBuilderClaim })
	if err := ValidateNew(up); err == nil {
		t.Error("a builder claim cannot carry approved trust")
	}
}

func TestValidateNewRules(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(*Entry)
		want string
	}{
		"bad id":                {func(e *Entry) { e.ID = "x" }, "id"},
		"bad run":               {func(e *Entry) { e.RunID = "" }, "run"},
		"empty task":            {func(e *Entry) { e.TaskID = "" }, "task"},
		"unknown kind":          {func(e *Entry) { e.Kind = "gossip" }, "kind"},
		"unknown trust":         {func(e *Entry) { e.Trust = "certain" }, "trust"},
		"decision not approved": {func(e *Entry) { e.Trust = TrustClaim }, "trust"},
		"check not observed":    {func(e *Entry) { e.Kind = KindCheckOutcome; e.Trust = TrustApproved }, "trust"},
		"starts superseded":     {func(e *Entry) { e.State = StateSuperseded }, "state"},
		"short source":          {func(e *Entry) { e.SourceHash = "abc" }, "source"},
		"no validity":           {func(e *Entry) { e.ValidityHash = "" }, "validity"},
		"no value":              {func(e *Entry) { e.ValueHash = "" }, "value"},
		"no scope hash":         {func(e *Entry) { e.ScopeHash = "" }, "scope"},
		"self supersession":     {func(e *Entry) { e.SupersedesID = e.ID }, "itself"},
		"escaping scope":        {func(e *Entry) { e.Scope = []string{"../x"} }, "escapes"},
		"repeated dependency":   {func(e *Entry) { e.Dependencies = []string{"a", "a"} }, "twice"},
	} {
		e := entry(1, c.mut)
		err := ValidateNew(e)
		if err == nil || !errors.Is(err, workflow.ErrInvalidTransition) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestMemoryClaimCannotSupersedeApprovedDecision(t *testing.T) {
	approved := entry(1)
	claim := entry(2, func(e *Entry) { e.Kind = KindApprovedDecision; e.Trust = TrustClaim; e.SupersedesID = approved.ID })
	if err := CanSupersede(approved, claim); err == nil || !errors.Is(err, workflow.ErrInvalidTransition) || !strings.Contains(err.Error(), "trust") {
		t.Errorf("a lower trust entry superseded an approved decision: %v", err)
	}
	same := entry(3, func(e *Entry) { e.SupersedesID = approved.ID })
	if err := CanSupersede(approved, same); err != nil {
		t.Errorf("equal trust may supersede: %v", err)
	}
	// An approved decision supersedes a claim of the same kind and scope.
	oldClaim := entry(4, func(e *Entry) { e.Kind = KindUnresolvedIssue; e.Trust = TrustClaim })
	newer := entry(5, func(e *Entry) { e.Kind = KindUnresolvedIssue; e.Trust = TrustApproved; e.SupersedesID = oldClaim.ID })
	if err := CanSupersede(oldClaim, newer); err != nil {
		t.Errorf("approved over claim: %v", err)
	}
	for name, mut := range map[string]func(*Entry){
		"other kind":  func(e *Entry) { e.Kind = KindInterfaceContract; e.Trust = TrustApproved },
		"other scope": func(e *Entry) { e.ScopeHash = hex64(1001) },
		"wrong link":  func(e *Entry) { e.SupersedesID = hex32(99) },
	} {
		n := entry(6, func(e *Entry) { e.SupersedesID = approved.ID })
		mut(&n)
		if err := CanSupersede(approved, n); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
	gone := entry(7, func(e *Entry) { e.State = StateInvalid })
	if err := CanSupersede(gone, entry(8, func(e *Entry) { e.SupersedesID = gone.ID })); err == nil {
		t.Error("an invalid entry cannot be superseded")
	}
}

func TestNewerIsNotMoreTrusted(t *testing.T) {
	// Without an explicit link, a later entry of the same kind and scope does not
	// displace an earlier one: both stay, and if required they conflict.
	older := entry(1, func(e *Entry) { e.Required = true })
	newer := entry(2, func(e *Entry) { e.Required = true; e.Value = "later" })
	if _, err := Select([]Entry{older, newer}, consumer, valid()); !errors.Is(err, workflow.ErrMemoryConflict) {
		t.Errorf("%v", err)
	}
}

func TestSelectIsPureAndDeterministic(t *testing.T) {
	entries := []Entry{entry(3), entry(1), entry(2, func(e *Entry) { e.TaskID = "x"; e.Scope = []string{"internal"} })}
	a, _ := Select(entries, consumer, valid())
	rev := []Entry{entries[2], entries[1], entries[0]}
	b, _ := Select(rev, consumer, valid())
	if ids(a) != ids(b) || ids(a) != "01,03,02" {
		t.Errorf("%s vs %s", ids(a), ids(b))
	}
	if entries[0].ID != hex32(3) {
		t.Error("Select reordered its input")
	}
}
