package state

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// collabEnv is a migrated store with two runs (a run and its replacement) and
// an open collaboration whose policy is a real artifact.
type collabEnv struct {
	s          *Store
	root       string
	a, b       *seed
	collabID   string
	policyHash string
}

func newCollabEnv(t *testing.T) *collabEnv {
	t.Helper()
	s, a, root := newSeededStore(t)
	b := seedProjectRunTaskAttempt(t, s, 2) // newSeededStore uses variant 1
	e := &collabEnv{s: s, root: root, a: a, b: b, collabID: hexID(900), policyHash: a.artifact(t, "policy v1")}
	if err := s.CreateCollaboration(ctx, CollaborationRequest{ID: e.collabID, PolicyHash: e.policyHash,
		InputCeilings: map[string]int64{"claude": 5000, "codex": 9000}, OutputCeilings: map[string]int64{"claude": 500}}); err != nil {
		t.Fatal(err)
	}
	return e
}

// work registers origin work that is not yet bound to a run task.
func (e *collabEnv) work(t *testing.T, id, group, alias string) {
	t.Helper()
	if err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: id, BudgetGroupID: group,
		Alias: alias}); err != nil {
		t.Fatal(err)
	}
}

// boundWork registers origin work as task-one of the first run.
func (e *collabEnv) boundWork(t *testing.T, id, group, alias string) {
	t.Helper()
	if err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: id, BudgetGroupID: group,
		Alias: alias, RunID: e.a.RunID, TaskID: e.a.TaskID}); err != nil {
		t.Fatal(err)
	}
}

func (e *collabEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *collabEnv) groups(t *testing.T, workID string) []string {
	t.Helper()
	w, err := e.s.LogicalWorkByID(ctx, e.collabID, workID)
	if err != nil {
		t.Fatal(err)
	}
	return w.BudgetGroups
}

func TestCollaborationAliasesAcrossReplacementRunsKeepOneIdentity(t *testing.T) {
	e := newCollabEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	// The replacement run renames the task; the binding is an alias, not new work.
	if err := e.s.AttachWorkAlias(ctx, AliasRequest{CollaborationID: e.collabID, LogicalWorkID: hexID(901),
		Alias: "renamed-task-1", RunID: e.b.RunID, TaskID: e.b.TaskID}); err != nil {
		t.Fatal(err)
	}
	seedQuery := `SELECT COUNT(DISTINCT logical_work_id) FROM work_aliases
		WHERE collaboration_id = ? AND alias IN ('task-1', 'renamed-task-1')`
	if n := e.count(t, seedQuery, e.collabID); n != 1 {
		t.Fatalf("aliases map to %d logical works, want 1", n)
	}
	e.s.Close()
	e.s = openStore(t, e.root)
	if n := e.count(t, seedQuery, e.collabID); n != 1 {
		t.Fatalf("after reopening, aliases map to %d logical works", n)
	}
	for _, alias := range []string{"task-1", "renamed-task-1"} {
		w, err := e.s.ResolveWork(ctx, e.collabID, alias)
		if err != nil {
			t.Fatal(err)
		}
		if w.ID != hexID(901) || w.OriginName != "task-1" || !sameSet(w.BudgetGroups, []string{hexID(951)}) {
			t.Fatalf("%s resolved to %+v", alias, w)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM work_task_bindings WHERE logical_work_id=?`, hexID(901)); n != 2 {
		t.Fatalf("%d run/task bindings, want one per run", n)
	}
}

func TestCollaborationUnknownMergeIsAMappingError(t *testing.T) {
	e := newCollabEnv(t)
	e.work(t, hexID(901), hexID(951), "task-1")
	before := e.count(t, `SELECT count(*) FROM logical_work`)
	err := e.s.MergeWork(ctx, MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(901), hexID(999)},
		ID: hexID(903), Alias: "merged"})
	if !errors.Is(err, ErrWorkMapping) {
		t.Fatalf("merge with an unknown source: %v", err)
	}
	if after := e.count(t, `SELECT count(*) FROM logical_work`); after != before {
		t.Fatalf("a refused merge wrote %d works", after-before)
	}
	if _, err := e.s.ResolveWork(ctx, e.collabID, "never-registered"); !errors.Is(err, ErrWorkMapping) {
		t.Fatalf("an unknown alias: %v", err)
	}
}

func TestCollaborationRenameSplitMergeAndReplaceMintNoAllowance(t *testing.T) {
	e := newCollabEnv(t)
	e.work(t, hexID(901), hexID(951), "task-1")
	e.work(t, hexID(902), hexID(952), "task-2")
	groups := func() int {
		return e.count(t, `SELECT count(*) FROM budget_groups WHERE collaboration_id=?`, e.collabID)
	}
	if groups() != 2 {
		t.Fatalf("%d groups for two origin works", groups())
	}
	if err := e.s.AttachWorkAlias(ctx, AliasRequest{CollaborationID: e.collabID, LogicalWorkID: hexID(901),
		Alias: "task-1-replacement", RunID: e.b.RunID, TaskID: e.b.TaskID}); err != nil {
		t.Fatal(err)
	}
	split := SplitRequest{CollaborationID: e.collabID, ParentID: hexID(901),
		Children: []WorkChild{{ID: hexID(911), Alias: "task-1a"}, {ID: hexID(912), Alias: "task-1b"}}}
	if err := e.s.SplitWork(ctx, split); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SplitWork(ctx, split); err != nil {
		t.Fatalf("retrying the same split: %v", err)
	}
	merge := MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(912), hexID(902)}, ID: hexID(921), Alias: "merged"}
	if err := e.s.MergeWork(ctx, merge); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MergeWork(ctx, merge); err != nil {
		t.Fatalf("retrying the same merge: %v", err)
	}
	if groups() != 2 {
		t.Fatalf("renaming, splitting, merging and replacing minted groups: %d", groups())
	}
	for id, want := range map[string][]string{hexID(911): {hexID(951)}, hexID(912): {hexID(951)},
		hexID(921): {hexID(951), hexID(952)}} {
		if got := e.groups(t, id); !sameSet(got, want) {
			t.Errorf("work %s groups %v, want %v", id, got, want)
		}
	}
	for _, id := range []string{hexID(901), hexID(902), hexID(912)} {
		w, _ := e.s.LogicalWorkByID(ctx, e.collabID, id)
		if w.State != "superseded" {
			t.Errorf("work %s is %q after being split or merged", id, w.State)
		}
	}
	// A superseded source cannot be merged again to obtain a fresh identity.
	err := e.s.MergeWork(ctx, MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(901), hexID(911)},
		ID: hexID(922), Alias: "again"})
	if !errors.Is(err, ErrWorkMapping) {
		t.Fatalf("merging superseded work: %v", err)
	}
}

func TestCollaborationAmbiguousSuccessorPausesForReconciliation(t *testing.T) {
	e := newCollabEnv(t)
	e.work(t, hexID(901), hexID(951), "task-1")
	e.work(t, hexID(902), hexID(952), "task-2")
	if err := e.s.SplitWork(ctx, SplitRequest{CollaborationID: e.collabID, ParentID: hexID(901),
		Children: []WorkChild{{ID: hexID(911), Alias: "task-1a"}, {ID: hexID(912), Alias: "task-1b"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ResolveWork(ctx, e.collabID, "task-1"); !errors.Is(err, ErrWorkMapping) {
		t.Fatalf("an alias of split work resolved without reconciliation: %v", err)
	}
	if err := e.s.MergeWork(ctx, MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(902)},
		ID: hexID(921), Alias: "task-2-only"}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a one-source merge: %v", err)
	}
	if err := e.s.MergeWork(ctx, MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(911), hexID(902)},
		ID: hexID(921), Alias: "merged"}); err != nil {
		t.Fatal(err)
	}
	// One successor is unambiguous: the merged work's old alias follows it.
	w, err := e.s.ResolveWork(ctx, e.collabID, "task-2")
	if err != nil || w.ID != hexID(921) {
		t.Fatalf("task-2 resolved to %+v, %v", w, err)
	}
}

func TestCollaborationAliasAndBindingConflicts(t *testing.T) {
	e := newCollabEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	if err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: hexID(902), BudgetGroupID: hexID(952),
		Alias: "task-1"}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an alias reused for other work: %v", err)
	}
	if err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: hexID(903), BudgetGroupID: hexID(953),
		Alias: "other", RunID: e.a.RunID, TaskID: e.a.TaskID}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a run task bound to two works: %v", err)
	}
	if err := e.s.AttachWorkAlias(ctx, AliasRequest{CollaborationID: e.collabID, LogicalWorkID: hexID(901),
		Alias: "task-1"}); err != nil {
		t.Fatalf("re-attaching the same alias: %v", err)
	}
	if err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: hexID(901), BudgetGroupID: hexID(951),
		Alias: "task-1", RunID: e.a.RunID, TaskID: e.a.TaskID}); err != nil {
		t.Fatalf("retrying the same work registration: %v", err)
	}
	if err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: hexID(901), BudgetGroupID: hexID(959),
		Alias: "task-1"}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("the same work id with another budget group: %v", err)
	}
}

func TestCollaborationCreateValidatesAndIsIdempotent(t *testing.T) {
	e := newCollabEnv(t)
	same := CollaborationRequest{ID: e.collabID, PolicyHash: e.policyHash,
		InputCeilings: map[string]int64{"claude": 5000, "codex": 9000}, OutputCeilings: map[string]int64{"claude": 500}}
	if err := e.s.CreateCollaboration(ctx, same); err != nil {
		t.Fatalf("an identical create: %v", err)
	}
	changed := same
	changed.InputCeilings = map[string]int64{"claude": 6000, "codex": 9000}
	if err := e.s.CreateCollaboration(ctx, changed); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a changed create under the same id: %v", err)
	}
	for name, req := range map[string]CollaborationRequest{
		"bad id":           {ID: "x", PolicyHash: e.policyHash},
		"bad hash":         {ID: hexID(905), PolicyHash: "abc"},
		"negative ceiling": {ID: hexID(905), PolicyHash: e.policyHash, InputCeilings: map[string]int64{"codex": -1}},
		"empty provider":   {ID: hexID(905), PolicyHash: e.policyHash, OutputCeilings: map[string]int64{"": 1}},
	} {
		if err := e.s.CreateCollaboration(ctx, req); !errors.Is(err, workflow.ErrInvalidTransition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := e.s.CreateCollaboration(ctx, CollaborationRequest{ID: hexID(905), PolicyHash: hash64("no such artifact")}); err == nil {
		t.Fatal("a policy hash that is not a stored artifact was accepted")
	}
	var input, output sql.NullInt64
	if err := e.s.db.QueryRow(`SELECT input_ceiling, output_ceiling FROM provider_budgets WHERE collaboration_id=? AND provider='codex'`,
		e.collabID).Scan(&input, &output); err != nil {
		t.Fatal(err)
	}
	if input.Int64 != 9000 || !input.Valid || output.Valid {
		t.Fatalf("codex ceilings %v/%v: an undeclared output ceiling must stay null, not zero", input, output)
	}
	if n := e.count(t, `SELECT count(*) FROM artifact_refs WHERE owner_kind='collaboration' AND owner_id=? AND role='policy' AND hash=?`,
		e.collabID, e.policyHash); n != 1 {
		t.Fatal("the policy artifact has no artifact_refs row")
	}
}

func TestCollaborationWorkNeedsAnOpenCollaboration(t *testing.T) {
	e := newCollabEnv(t)
	if _, err := e.s.db.Exec(`UPDATE collaborations SET status='closing' WHERE id=?`, e.collabID); err != nil {
		t.Fatal(err)
	}
	err := e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: e.collabID, ID: hexID(901), BudgetGroupID: hexID(951), Alias: "late"})
	if !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("work added to a closing collaboration: %v", err)
	}
	err = e.s.CreateLogicalWork(ctx, WorkRequest{CollaborationID: hexID(777), ID: hexID(901), BudgetGroupID: hexID(951), Alias: "x"})
	if !errors.Is(err, ErrWorkMapping) {
		t.Fatalf("work for an unknown collaboration: %v", err)
	}
}

func sameSet(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
