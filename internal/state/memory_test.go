package state

import (
	"errors"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/workflow"
)

// memEnv is a run with two tasks and a way to build entries whose hashes point
// at real artifacts.
type memEnv struct {
	t     *testing.T
	s     *Store
	sd    *seed
	run   string
	n     int
	scope string
}

func newMemEnv(t *testing.T) *memEnv {
	t.Helper()
	s, sd, _ := newSeededStore(t)
	spec := runSpec(t, s, 700)
	if err := s.CreateRun(ctx, spec); err != nil {
		t.Fatal(err)
	}
	return &memEnv{t: t, s: s, sd: sd, run: spec.RunID, scope: sd.artifact(t, `["internal/state"]`)}
}

// entry is an approved decision by task-1 about internal/state.
func (m *memEnv) entry(mut ...func(*memory.Entry)) memory.Entry {
	m.t.Helper()
	m.n++
	e := memory.Entry{ID: hexID(9000 + m.n), RunID: m.run, TaskID: "task-1", Kind: memory.KindApprovedDecision, Trust: memory.TrustApproved,
		State: memory.StateActive, ScopeHash: m.scope, Scope: []string{"internal/state"},
		SourceHash: m.sd.artifact(m.t, "source "+hexID(m.n)), ValidityHash: m.sd.artifact(m.t, "validity v1"),
		ValueHash: m.sd.artifact(m.t, "value "+hexID(m.n))}
	for _, f := range mut {
		f(&e)
	}
	return e
}

func (m *memEnv) put(e memory.Entry) {
	m.t.Helper()
	if err := m.s.PutMemory(ctx, e); err != nil {
		m.t.Fatalf("PutMemory %s: %v", e.ID, err)
	}
}

func (m *memEnv) get(id string) memory.Entry {
	m.t.Helper()
	e, ok, err := m.s.GetMemory(ctx, id)
	if err != nil || !ok {
		m.t.Fatalf("GetMemory %s: %v %v", id, ok, err)
	}
	return e
}

func TestPutMemoryRoundTripsMetadataAndHashesNotText(t *testing.T) {
	m := newMemEnv(t)
	e := m.entry(func(e *memory.Entry) {
		e.Required = true
		e.Dependencies = []string{"task-2", "task-1"}
		e.Value = "must never reach SQL"
	})
	m.put(e)
	got := m.get(e.ID)
	if got.RunID != m.run || got.TaskID != "task-1" || got.Kind != e.Kind || got.Trust != e.Trust || got.State != memory.StateActive || !got.Required ||
		got.ScopeHash != e.ScopeHash || got.SourceHash != e.SourceHash || got.ValidityHash != e.ValidityHash || got.ValueHash != e.ValueHash {
		t.Fatalf("%+v", got)
	}
	if strings.Join(got.Dependencies, ",") != "task-1,task-2" {
		t.Errorf("dependencies %v", got.Dependencies)
	}
	if got.Value != "" || got.Scope != nil {
		t.Errorf("the store must not hold the full text: value %q scope %v", got.Value, got.Scope)
	}
	var n int
	if err := m.s.db.QueryRow(`SELECT count(*) FROM memory_entries WHERE id=?`, e.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// Every hash is referenced, so garbage collection cannot drop what an entry points at.
	var refs int
	m.s.db.QueryRow(`SELECT count(*) FROM artifact_refs WHERE owner_kind='memory' AND owner_id=?`, e.ID).Scan(&refs)
	if refs != 4 {
		t.Errorf("%d artifact refs, want scope, source, validity and value", refs)
	}
}

func TestPutMemoryIsReplaySafe(t *testing.T) {
	m := newMemEnv(t)
	e := m.entry()
	m.put(e)
	if err := m.s.PutMemory(ctx, e); err != nil {
		t.Fatalf("recording the same entry again: %v", err)
	}
	if n := m.sd.count(t, "memory_entries"); n != 1 {
		t.Errorf("%d rows", n)
	}
	other := e
	other.ValueHash = m.sd.artifact(t, "a different value")
	if err := m.s.PutMemory(ctx, other); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("the same id with different content: %v", err)
	}
}

func TestPutMemoryRejectionsWriteNothing(t *testing.T) {
	m := newMemEnv(t)
	missing := hexID(77777)
	for name, c := range map[string]struct {
		mut  func(*memory.Entry)
		want error
	}{
		"unknown run":           {func(e *memory.Entry) { e.RunID = hexID(404) }, workflow.ErrConflict},
		"task not in the run":   {func(e *memory.Entry) { e.TaskID = "task-9" }, workflow.ErrConflict},
		"dependency not in run": {func(e *memory.Entry) { e.Dependencies = []string{"task-9"} }, workflow.ErrConflict},
		"unregistered artifact": {func(e *memory.Entry) { e.ValueHash = strings.Repeat("a", 64) }, workflow.ErrIncompleteEvidence},
		"claim required": {func(e *memory.Entry) {
			e.Kind = memory.KindBuilderClaim
			e.Trust = memory.TrustClaim
			e.Required = true
		}, workflow.ErrInvalidTransition},
		"claim as approved":  {func(e *memory.Entry) { e.Kind = memory.KindBuilderClaim }, workflow.ErrInvalidTransition},
		"supersedes nothing": {func(e *memory.Entry) { e.SupersedesID = missing }, workflow.ErrConflict},
	} {
		before := m.sd.count(t, "memory_entries")
		err := m.s.PutMemory(ctx, m.entry(c.mut))
		if err == nil || !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if m.sd.count(t, "memory_entries") != before || m.sd.count(t, "memory_dependencies") != 0 {
			t.Errorf("%s: a rejected entry left rows behind", name)
		}
	}
}

func TestSupersessionIsRecordedNotOverwritten(t *testing.T) {
	m := newMemEnv(t)
	old := m.entry(func(e *memory.Entry) { e.Kind = memory.KindUnresolvedIssue; e.Trust = memory.TrustClaim })
	m.put(old)
	next := m.entry(func(e *memory.Entry) {
		e.Kind = memory.KindUnresolvedIssue
		e.Trust = memory.TrustApproved
		e.SupersedesID = old.ID
	})
	m.put(next)
	if got := m.get(old.ID); got.State != memory.StateSuperseded || got.ValueHash != old.ValueHash {
		t.Errorf("the old entry must be marked superseded with its history kept: %+v", got)
	}
	if got := m.get(next.ID); got.State != memory.StateActive || got.SupersedesID != old.ID {
		t.Errorf("%+v", got)
	}
	// A superseded entry cannot be superseded again, which would fork the chain.
	again := m.entry(func(e *memory.Entry) {
		e.Kind = memory.KindUnresolvedIssue
		e.Trust = memory.TrustApproved
		e.SupersedesID = old.ID
	})
	if err := m.s.PutMemory(ctx, again); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("%v", err)
	}
}

func TestMemoryClaimCannotSupersedeApprovedDecision(t *testing.T) {
	m := newMemEnv(t)
	approved := m.entry()
	m.put(approved)
	for name, mut := range map[string]func(*memory.Entry){
		"lower trust": func(e *memory.Entry) { e.Trust = memory.TrustObserved; e.Kind = memory.KindApprovedDecision },
		"other kind":  func(e *memory.Entry) { e.Kind = memory.KindInterfaceContract },
		"other scope": func(e *memory.Entry) { e.ScopeHash = m.sd.artifact(t, `["cmd"]`) },
	} {
		e := m.entry(func(e *memory.Entry) { e.SupersedesID = approved.ID }, mut)
		if err := m.s.PutMemory(ctx, e); !errors.Is(err, workflow.ErrInvalidTransition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := m.get(approved.ID); got.State != memory.StateActive {
		t.Errorf("a rejected supersession changed the approved decision: %+v", got)
	}
	// A claim that carries claim trust is rejected before it can name anything.
	claim := m.entry(func(e *memory.Entry) {
		e.Kind = memory.KindBuilderClaim
		e.Trust = memory.TrustClaim
		e.SupersedesID = approved.ID
	})
	if err := m.s.PutMemory(ctx, claim); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("%v", err)
	}
	if m.sd.count(t, "memory_entries") != 1 {
		t.Error("a rejected claim was stored")
	}
}

func TestMemorySupersessionCycleRejected(t *testing.T) {
	m := newMemEnv(t)
	a := m.entry()
	m.put(a)
	b := m.entry(func(e *memory.Entry) { e.SupersedesID = a.ID })
	m.put(b)
	// A malformed chain, as only corruption could produce: A now points back at B.
	m.sd.exec(t, `UPDATE memory_entries SET supersedes_id=? WHERE id=?`, b.ID, a.ID)

	c := m.entry(func(e *memory.Entry) { e.SupersedesID = b.ID })
	err := m.s.PutMemory(ctx, c)
	if !errors.Is(err, workflow.ErrInvalidTransition) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("%v", err)
	}
	if m.sd.count(t, "memory_entries") != 2 {
		t.Error("the rejected entry was stored")
	}
	if got := m.get(b.ID); got.State != memory.StateActive {
		t.Errorf("the transaction did not roll back: %+v", got)
	}
}

func TestMemoryDependencyChangeInvalidates(t *testing.T) {
	m := newMemEnv(t)
	stale := m.entry(func(e *memory.Entry) { e.Required = true })
	fresh := m.entry(func(e *memory.Entry) { e.ValidityHash = m.sd.artifact(t, "validity v2") })
	superseded := m.entry()
	m.put(stale)
	m.put(fresh)
	m.put(superseded)
	m.put(m.entry(func(e *memory.Entry) { e.SupersedesID = superseded.ID }))

	v2 := m.get(fresh.ID).ValidityHash
	ids, err := m.s.InvalidateMemory(ctx, m.run, map[string]bool{v2: true})
	if err != nil {
		t.Fatal(err)
	}
	// "stale" and the replacement of "superseded" share validity v1; the entry
	// already superseded is history and is left as it was.
	if len(ids) != 2 || ids[0] != stale.ID {
		t.Errorf("invalidated %v", ids)
	}
	got := m.get(stale.ID)
	if got.State != memory.StateInvalid || got.ValueHash != stale.ValueHash || got.SourceHash != stale.SourceHash || !got.Required {
		t.Errorf("an invalidated entry keeps its history: %+v", got)
	}
	if m.get(fresh.ID).State != memory.StateActive || m.get(superseded.ID).State != memory.StateSuperseded {
		t.Error("an entry whose binding still holds, or one already superseded, was changed")
	}
	if n := m.sd.count(t, "memory_entries"); n != 4 {
		t.Errorf("%d rows: nothing is deleted", n)
	}
	again, err := m.s.InvalidateMemory(ctx, m.run, map[string]bool{v2: true})
	if err != nil || len(again) != 0 {
		t.Errorf("a second pass: %v %v", again, err)
	}
	// And it feeds selection.
	all, err := m.s.ListMemory(ctx, m.run)
	if err != nil || len(all) != 4 {
		t.Fatalf("%v %v", all, err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Error("ListMemory must be ordered by id")
		}
	}
}

func TestListMemoryIsScopedToTheRun(t *testing.T) {
	m := newMemEnv(t)
	m.put(m.entry())
	if got, err := m.s.ListMemory(ctx, hexID(808)); err != nil || len(got) != 0 {
		t.Errorf("another run's memory: %v %v", got, err)
	}
	if _, ok, err := m.s.GetMemory(ctx, hexID(808)); ok || err != nil {
		t.Errorf("a missing id: %v %v", ok, err)
	}
}
