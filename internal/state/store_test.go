package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
)

func newRoot(t *testing.T) string { return filepath.Join(t.TempDir(), "checkout-state") }

func openStore(t *testing.T, root string) *Store {
	t.Helper()
	s, err := Open(context.Background(), root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// insertArtifact writes one valid artifacts row through the store's own handle.
func insertArtifact(t *testing.T, s *Store, hash string) error {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,?,?)`, hash, 1, "text/plain", 1)
	return err
}

func hash64(c string) string {
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestStoreOpenReopenAndForeignKeys(t *testing.T) {
	root := newRoot(t)
	s := openStore(t, root)
	if v, err := s.SchemaVersion(); err != nil || v != LatestSchema() {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, LatestSchema())
	}
	h := hash64("kept")
	if err := insertArtifact(t, s, h); err != nil {
		t.Fatal(err)
	}
	// A run that names a project and artifacts that do not exist.
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO projects(id,checkout_id,manifest_root) VALUES('p','missing-checkout','.')`)
	if err == nil {
		t.Fatal("a dangling foreign key was accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openStore(t, root)
	var n int
	if err := s2.db.QueryRow(`SELECT count(*) FROM artifacts WHERE hash=?`, h).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after reopen: n=%d err=%v", n, err)
	}
	if err := s2.db.QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the rejected insert left a row: %d %v", n, err)
	}
}

// Pool-level PRAGMAs vanish with the connection. This discards the live
// connection and checks the replacement is configured too.
func TestStoreForeignKeysAfterReconnect(t *testing.T) {
	s := openStore(t, newRoot(t))
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `CREATE TEMP TABLE probe(x)`); err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); err != nil && !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	conn.Close()

	var temp int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_temp_master WHERE name='probe'`).Scan(&temp); err != nil {
		t.Fatal(err)
	}
	if temp != 0 {
		t.Fatal("the connection was not replaced, so this proves nothing")
	}
	var fk, busy, sync int
	for q, dst := range map[string]*int{"PRAGMA foreign_keys": &fk, "PRAGMA busy_timeout": &busy, "PRAGMA synchronous": &sync} {
		if err := s.db.QueryRowContext(ctx, q).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if fk != 1 || busy != 5000 || sync != 2 {
		t.Fatalf("new connection: foreign_keys=%d busy_timeout=%d synchronous=%d, want 1, 5000, 2 (FULL)", fk, busy, sync)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO projects(id,checkout_id,manifest_root) VALUES('p','nope','.')`); err == nil {
		t.Fatal("foreign keys stopped being enforced after the reconnect")
	}
}

func TestStoreUsesWALAndOnePrivateConnection(t *testing.T) {
	root := newRoot(t)
	s := openStore(t, root)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	for path, want := range map[string]os.FileMode{root: 0o700, filepath.Join(root, "state.sqlite"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v err %v, want %v", path, info.Mode().Perm(), err, want)
		}
	}
}

func TestTwoStoresWriteTheSameDatabase(t *testing.T) {
	root := newRoot(t)
	a := openStore(t, root)
	b := openStore(t, root)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				errs <- insertArtifact(t, s, hash64(string(rune('a'+i))+string(rune('A'+j))))
			}
		}(i, s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent writer failed: %v", err)
		}
	}
	var n int
	if err := a.db.QueryRow(`SELECT count(*) FROM artifacts`).Scan(&n); err != nil || n != 40 {
		t.Fatalf("rows = %d, %v", n, err)
	}
}

// rawVersion makes a database file with the given user_version and nothing else.
func rawVersion(t *testing.T, root string, version int) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openRaw(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE future(x); PRAGMA user_version = ` + strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
}

// openRaw opens a database file with no tandem setup at all.
func openRaw(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }

func TestReadOnlyStatusDoesNotCreateStore(t *testing.T) {
	ctx := context.Background()
	t.Run("missing root", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "absent")
		before := listDir(t, parent)
		s, err := OpenReadOnly(ctx, root)
		if err == nil || s != nil {
			t.Fatalf("OpenReadOnly of nothing = %v, %v", s, err)
		}
		if !errors.Is(err, ErrNoStore) {
			t.Fatalf("err = %v, want ErrNoStore", err)
		}
		if after := listDir(t, parent); len(after) != len(before) {
			t.Fatalf("created files: %v", after)
		}
	})
	t.Run("root without a database", func(t *testing.T) {
		root := t.TempDir()
		if _, err := OpenReadOnly(ctx, root); !errors.Is(err, ErrNoStore) {
			t.Fatalf("err = %v", err)
		}
		if got := listDir(t, root); len(got) != 1 {
			t.Fatalf("created files: %v", got)
		}
	})
	t.Run("newer schema", func(t *testing.T) {
		root := newRoot(t)
		rawVersion(t, root, LatestSchema()+3)
		db := filepath.Join(root, "state.sqlite")
		beforeHash, beforeFiles := fileHash(t, db), listDir(t, root)
		s, err := OpenReadOnly(ctx, root)
		var newer *NewerSchemaError
		if s != nil || !errors.Is(err, ErrNewerSchema) || !errors.As(err, &newer) {
			t.Fatalf("OpenReadOnly = %v, %v", s, err)
		}
		if newer.Found != LatestSchema()+3 || newer.Supported != LatestSchema() {
			t.Fatalf("diagnostic %+v", newer)
		}
		if fileHash(t, db) != beforeHash || len(listDir(t, root)) != len(beforeFiles) {
			t.Fatal("a refused read-only open changed the store")
		}
	})
}

func TestOpenRefusesANewerSchemaWithoutChangingIt(t *testing.T) {
	root := newRoot(t)
	rawVersion(t, root, LatestSchema()+1)
	db := filepath.Join(root, "state.sqlite")
	before := fileHash(t, db)
	s, err := Open(context.Background(), root)
	if s != nil || !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Open = %v, %v", s, err)
	}
	if fileHash(t, db) != before {
		t.Fatal("refusing a newer schema modified the file")
	}
	if backups, _ := filepath.Glob(filepath.Join(root, "backups", "*")); len(backups) != 0 {
		t.Fatalf("a refused open made backups: %v", backups)
	}
}

func TestReadOnlyOpenReadsAndRefusesWrites(t *testing.T) {
	root := newRoot(t)
	w := openStore(t, root)
	if err := insertArtifact(t, w, hash64("x")); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM artifacts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := insertArtifact(t, r, hash64("y")); err == nil {
		t.Fatal("a read-only store accepted a write")
	}
}
