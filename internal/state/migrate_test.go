package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

var errInjected = errors.New("injected failure before commit")

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestEmbeddedMigrationsAreOrderedAndComplete(t *testing.T) {
	ms, err := loadMigrations(embedded)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].version != 1 {
		t.Fatalf("migrations %+v", ms)
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migration %d has version %d: versions must be contiguous from 1", i, m.version)
		}
	}
	s := openStore(t, newRoot(t))
	for _, table := range []string{"checkouts", "projects", "artifacts", "runs", "tasks", "task_dependencies", "attempts"} {
		if !tableExists(t, s, table) {
			t.Errorf("table %s missing after the first migration", table)
		}
	}
}

func TestLoadMigrationsRejectsGapsAndBadNames(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"gap":      {"migrations/001_a.sql": {Data: []byte("")}, "migrations/003_c.sql": {Data: []byte("")}},
		"no start": {"migrations/002_b.sql": {Data: []byte("")}},
		"bad name": {"migrations/first.sql": {Data: []byte("")}},
		"dup":      {"migrations/001_a.sql": {Data: []byte("")}, "migrations/001_b.sql": {Data: []byte("")}},
	} {
		if _, err := loadMigrations(files); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A first-ever migration that fails right before COMMIT leaves an empty
// database at version 0, not half a schema.
func TestMigrationRollbackKeepsVersion(t *testing.T) {
	root := newRoot(t)
	hooks := testHooks{beforeCommit: func(int) error { return errInjected }}
	if _, err := open(context.Background(), root, embedded, hooks); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v", err)
	}
	db, err := openRaw(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v, tables int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables)
	if v != 0 || tables != 0 {
		t.Fatalf("after the failed migration: version %d, %d tables", v, tables)
	}
	// A clean retry succeeds from that state.
	s := openStore(t, root)
	if got, _ := s.SchemaVersion(); got != LatestSchema() {
		t.Fatalf("retry version %d", got)
	}
}

func upgradeFS(extra string) fstest.MapFS {
	files := fstest.MapFS{}
	entries, _ := embedded.ReadDir("migrations")
	for _, e := range entries {
		raw, _ := embedded.ReadFile("migrations/" + e.Name())
		files["migrations/"+e.Name()] = &fstest.MapFile{Data: raw}
	}
	files[fmt.Sprintf("migrations/%03d_extra.sql", LatestSchema()+1)] = &fstest.MapFile{Data: []byte(extra)}
	return files
}

// Upgrading an existing store backs it up first, and a failed upgrade keeps
// the prior version, the prior tables and the backup.
func TestFailedUpgradeKeepsThePriorSchemaAndABackup(t *testing.T) {
	root := newRoot(t)
	s := openStore(t, root)
	if err := insertArtifact(t, s, hash64("precious")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	files := upgradeFS(`CREATE TABLE added_later(x INTEGER); INSERT INTO added_later VALUES(1);`)
	latest := LatestSchema()
	hooks := testHooks{beforeCommit: func(v int) error {
		if v == latest+1 {
			return errInjected
		}
		return nil
	}}
	if _, err := open(context.Background(), root, files, hooks); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v", err)
	}
	prior := openStore(t, root) // the embedded set still supports only latest
	if v, _ := prior.SchemaVersion(); v != latest {
		t.Fatalf("version %d after the failed upgrade, want %d", v, latest)
	}
	if tableExists(t, prior, "added_later") {
		t.Fatal("a half-created table survived the rollback")
	}
	backups, _ := filepath.Glob(filepath.Join(root, "backups", fmt.Sprintf("state-v%d-*.sqlite", latest)))
	if len(backups) != 1 {
		t.Fatalf("backups %v, want exactly one retained", backups)
	}
	bdb, err := openRaw(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	var n, bv int
	bdb.QueryRow(`SELECT count(*) FROM artifacts`).Scan(&n)
	bdb.QueryRow(`PRAGMA user_version`).Scan(&bv)
	if n != 1 || bv != latest {
		t.Fatalf("backup holds %d artifacts at version %d, want 1 at %d", n, bv, latest)
	}
	if info, _ := os.Stat(backups[0]); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v, want 0600", info.Mode().Perm())
	}
}

func TestSuccessfulUpgradeAppliesInOrderAfterABackup(t *testing.T) {
	root := newRoot(t)
	openStore(t, root).Close()
	files := upgradeFS(`CREATE TABLE added_later(x INTEGER);`)
	s, err := open(context.Background(), root, files, testHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	latest := LatestSchema()
	if v, _ := s.SchemaVersion(); v != latest+1 || !tableExists(t, s, "added_later") {
		t.Fatalf("version %d, table %v", v, tableExists(t, s, "added_later"))
	}
	if backups, _ := filepath.Glob(filepath.Join(root, "backups", fmt.Sprintf("state-v%d-*.sqlite", latest))); len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
}

func TestFreshStoreMakesNoBackup(t *testing.T) {
	root := newRoot(t)
	openStore(t, root)
	if backups, _ := filepath.Glob(filepath.Join(root, "backups", "*")); len(backups) != 0 {
		t.Fatalf("a fresh store made backups: %v", backups)
	}
}

// Reopening hides a missing ROLLBACK, because closing a connection discards an
// open transaction. This inspects the same pool right after the failure.
func TestFailedMigrationLeavesNoOpenTransactionOnThePool(t *testing.T) {
	root := newRoot(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := newPool(filepath.Join(root, dbFile), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms, _ := loadMigrations(embedded)
	err = migrate(context.Background(), db, root, ms, testHooks{beforeCommit: func(int) error { return errInjected }})
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v", err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("the failed migration's tables are visible on the pool: %d, %v", tables, err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("the connection is still inside the failed transaction: %v", err)
	}
	conn.ExecContext(context.Background(), `ROLLBACK`)
}
