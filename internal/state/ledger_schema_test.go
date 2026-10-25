package state

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// Schema-level tests of migration 002: foreign keys, GC retention of ledger
// artifacts, and upgrading and refusing across schema versions.

func TestCollaborationForeignKeysRejectDanglingRows(t *testing.T) {
	e := newCollabEnv(t)
	e.work(t, hexID(901), hexID(951), "task-1")
	for name, q := range map[string][]any{
		"work without collaboration": {`INSERT INTO logical_work(id,collaboration_id,origin_name,state,created_ms) VALUES(?,?,?,'active',1)`, hexID(931), hexID(777), "x"},
		"alias without work":         {`INSERT INTO work_aliases(collaboration_id,alias,logical_work_id,created_ms) VALUES(?,?,?,1)`, e.collabID, "y", hexID(932)},
		"group link without group":   {`INSERT INTO work_budget_groups(logical_work_id,budget_group_id) VALUES(?,?)`, hexID(901), hexID(933)},
		"binding without task":       {`INSERT INTO work_task_bindings(run_id,task_id,logical_work_id) VALUES(?,?,?)`, e.a.RunID, "no-such-task", hexID(901)},
		"source without run":         {`INSERT INTO expected_sources(id,collaboration_id,provider,role,run_id,status,created_ms) VALUES(?,?,'codex','builder',?,'expected',1)`, hexID(934), e.collabID, hexID(778)},
		"segment without source":     {`INSERT INTO session_segments(id,collaboration_id,source_id,provider,session_id,role,opened_ms) VALUES(?,?,?,'claude','s','planner',1)`, hexID(935), e.collabID, hexID(936)},
		"snapshot without artifact":  {`INSERT INTO usage_snapshots(id,collaboration_id,revision,content_hash,complete,created_ms) VALUES(?,?,1,?,0,1)`, hash64("snap"), e.collabID, hash64("missing")},
		"policy without artifact":    {`INSERT INTO collaborations(id,policy_hash,status,created_ms) VALUES(?,?,'open',1)`, hexID(937), hash64("missing policy")},
	} {
		if _, err := e.s.db.Exec(q[0].(string), q[1:]...); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A pre-run source needs no run or attempt.
	if _, err := e.s.db.Exec(`INSERT INTO expected_sources(id,collaboration_id,provider,role,status,created_ms) VALUES(?,?,'claude','planner','expected',1)`,
		hexID(938), e.collabID); err != nil {
		t.Fatalf("a pre-run planner source: %v", err)
	}
	// Existing constraints still hold.
	if _, err := e.s.db.Exec(`UPDATE runs SET engine_version=3 WHERE id=?`, e.a.RunID); err == nil {
		t.Fatal("engine_version constraint lost")
	}
}

func TestArtifactsReferencedByTheLedgerSurviveGC(t *testing.T) {
	e := newCollabEnv(t)
	e.a.exec(t, `UPDATE runs SET state='finished'`)
	snap := e.a.artifact(t, `{"snapshot":"sealed"}`)
	e.a.exec(t, `INSERT INTO usage_snapshots(id,collaboration_id,revision,content_hash,complete,created_ms) VALUES(?,?,1,?,1,1)`,
		hash64("snapshot one"), e.collabID, snap)
	lone := e.a.artifact(t, "nothing refers to this")
	rep, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]string{"policy": e.policyHash, "snapshot": snap} {
		if has(rep.Candidates, h) {
			t.Errorf("the %s artifact is a GC candidate", name)
		}
	}
	if !has(rep.Deleted, lone) {
		t.Fatal("GC kept an artifact nothing refers to; the test proves nothing")
	}
}

// v1Only is the embedded migration set cut back to schema version 1: a tandem
// from before the collaboration ledger.
func v1Only(t *testing.T) fstest.MapFS {
	t.Helper()
	raw, err := embedded.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"migrations/001_initial.sql": {Data: raw}}
}

func TestMigrationFromAPopulatedSchemaOneKeepsEverything(t *testing.T) {
	root := newRoot(t)
	old, err := open(context.Background(), root, v1Only(t), testHooks{})
	if err != nil {
		t.Fatal(err)
	}
	seedProjectRunTaskAttempt(t, old, 0)
	tables := []string{"checkouts", "projects", "artifacts", "runs", "tasks", "attempts", "artifact_refs"}
	counts := func(s *Store) map[string]int {
		out := map[string]int{}
		for _, tbl := range tables {
			var n int
			if err := s.db.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out[tbl] = n
		}
		return out
	}
	before := counts(old)
	old.Close()

	s := openStore(t, root)
	if v, _ := s.SchemaVersion(); v != LatestSchema() || v < 2 {
		t.Fatalf("version %d after upgrade, latest %d", v, LatestSchema())
	}
	if after := counts(s); !equalCounts(before, after) {
		t.Fatalf("rows changed by the upgrade: %v -> %v", before, after)
	}
	for _, tbl := range []string{"collaborations", "logical_work", "work_aliases", "budget_groups", "work_budget_groups",
		"work_ancestry", "work_task_bindings", "provider_budgets", "expected_sources", "session_segments", "usage_snapshots"} {
		if !tableExists(t, s, tbl) {
			t.Errorf("table %s missing after the upgrade", tbl)
		}
	}
	s.Close()
	again := openStore(t, root)
	if v, _ := again.SchemaVersion(); v != LatestSchema() {
		t.Fatalf("reopening changed the version to %d", v)
	}
	if backups, _ := filepath.Glob(filepath.Join(root, "backups", "state-v1-*.sqlite")); len(backups) != 1 {
		t.Fatalf("backups %v: want the one taken before the upgrade and none on reopening", backups)
	}
}

func TestMigrationNewerSchemaIsRefusedByAnOlderReader(t *testing.T) {
	root := newRoot(t)
	openStore(t, root).Close()
	_, err := open(context.Background(), root, v1Only(t), testHooks{})
	var newer *NewerSchemaError
	if !errors.As(err, &newer) || newer.Found != LatestSchema() || newer.Supported != 1 {
		t.Fatalf("an older reader opened a newer store: %v", err)
	}
	if v, _ := openStore(t, root).SchemaVersion(); v != LatestSchema() {
		t.Fatalf("the refused open changed the version to %d", v)
	}
}

// At the schema level: a populated schema-1 store takes every ledger table,
// keeps its rows across a reopen, and GC keeps every artifact a ledger row
// points at.
func TestLedgerTablesSurviveReopenAndGC(t *testing.T) {
	e := newCollabEnv(t)
	e.a.exec(t, `UPDATE runs SET state='finished'`)
	doc := e.a.artifact(t, `{"snapshot":"final"}`)
	snap, _, err := e.s.PutUsageSnapshot(ctx, e.collabID, doc, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.CommitUsage(ctx, UsageCommit{Provider: "codex", SessionID: "s", Cursor: []byte(`{}`), Offset: 4, PrefixHash: "h",
		Events: []StoredEvent{{EventID: "seg0:0", JSON: []byte(`{}`)}}}); err != nil {
		t.Fatal(err)
	}
	lone := e.a.artifact(t, "nothing refers to this")
	e.s.Close()
	e.s = openStore(t, e.root)
	if got, err := e.s.UsageSnapshot(ctx, snap.ID); err != nil || got.ContentHash != doc {
		t.Fatalf("snapshot after reopening: %+v %v", got, err)
	}
	if events, _ := e.s.UsageEvents(ctx, "codex", "s"); len(events) != 1 {
		t.Fatalf("events after reopening: %d", len(events))
	}
	rep, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if has(rep.Candidates, doc) || has(rep.Candidates, e.policyHash) {
		t.Fatalf("GC collected a referenced ledger artifact: %v", rep.Candidates)
	}
	if !has(rep.Deleted, lone) {
		t.Fatal("GC kept an unreferenced artifact; the check proves nothing")
	}
}
