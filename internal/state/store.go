// Package state is the durable runner's storage: a versioned SQLite database
// per checkout. It uses the pure-Go modernc.org/sqlite driver, so building and
// testing need no CGO.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNoStore means there is no initialized state database at the root.
var ErrNoStore = errors.New("no state database")

const dbFile = "state.sqlite"

// Store is one open state database. It holds a single connection: writers on
// separate Store values, in this or another process, queue on SQLite's busy
// timeout instead of racing inside one pool.
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool

	// now is the clock for created_ms; tests inject a fixed one.
	now func() time.Time
	// hooks are test-only failpoints inside Apply's transaction.
	hooks applyHooks
	// importHooks are test-only failpoints inside ImportLegacy.
	importHooks importHooks
	// admitHooks are test-only barriers inside AdmitDispatch.
	admitHooks admitHooks
}

// pragmas applied to every connection the pool opens, through the driver's own
// connection initialization. A PRAGMA run once on the pool would be lost when
// the connection is replaced. journal_mode is not here: it persists in the
// file, and changing it must wait until the schema version has been checked.
const pragmas = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"

func dsn(path string, readOnly bool) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: pragmas}
	if readOnly {
		u.RawQuery += "&mode=ro"
	}
	return u.String()
}

func newPool(path string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Open opens the store under root, creating it and applying supported
// migrations. It refuses a database from a newer tandem without changing it.
func Open(ctx context.Context, root string) (*Store, error) {
	return open(ctx, root, embedded, testHooks{})
}

func open(ctx context.Context, root string, fsys fs.FS, h testHooks) (*Store, error) {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, dbFile)
	// Created here so the file is private from its first byte; SQLite gives its
	// WAL and shared-memory files the same mode.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := newPool(path, false)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, now: time.Now}
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }

	v, err := userVersion(ctx, db)
	if err != nil {
		return fail(err)
	}
	if v > len(ms) {
		return fail(&NewerSchemaError{Found: v, Supported: len(ms)})
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return fail(err)
	}
	if mode != "wal" {
		return fail(fmt.Errorf("could not enable WAL (journal_mode is %q)", mode))
	}
	if err := migrate(ctx, db, root, ms, h); err != nil {
		return fail(err)
	}
	return s, nil
}

// OpenReadOnly opens an existing store without creating a file, running a
// migration or changing the journal mode. A missing store is ErrNoStore. A
// newer schema is a *NewerSchemaError and no Store: this tandem cannot know
// that layout, so the caller may report the version and nothing else.
func OpenReadOnly(ctx context.Context, root string) (*Store, error) {
	path := filepath.Join(root, dbFile)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s", ErrNoStore, path)
		}
		return nil, err
	}
	db, err := newPool(path, true)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	v, err := userVersion(ctx, db)
	if err != nil {
		return fail(err)
	}
	switch {
	case v > LatestSchema():
		return fail(&NewerSchemaError{Found: v, Supported: LatestSchema()})
	case v == 0:
		return fail(fmt.Errorf("%w at %s: the file has no schema", ErrNoStore, path))
	}
	return &Store{db: db, path: path, readOnly: true, now: time.Now}, nil
}

// WithClock replaces the store's time source and returns the store. The
// controller injects its clock so leases, backoff and reconciliation can be
// driven without waiting; tests use a settable one.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

// NudgeFile is renamed into the state root each time a writable store closes.
// The relay bridge watches for it, so a planner command that wrote the run's
// state (stop, accept, review-record) is acted on at once instead of at the
// bridge's backstop.
const NudgeFile = "nudge"

// Close releases the database. The last close checkpoints the WAL. A store
// whose connection changed rows then leaves a nudge. One that wrote nothing
// leaves none, so a refused or read-only command changes nothing on disk.
// The store holds one connection, so total_changes() counts every row this
// Store wrote; a rolled-back write still counts, which costs one extra tick.
func (s *Store) Close() error {
	var changes int64
	if !s.readOnly {
		_ = s.db.QueryRow(`SELECT total_changes()`).Scan(&changes)
	}
	err := s.db.Close()
	if changes > 0 {
		nudge(filepath.Dir(s.path))
	}
	return err
}

// nudge renames a fresh file over NudgeFile, so a watcher sees it created.
// It is a signal only: not synced, and a failure is ignored.
func nudge(root string) {
	tmp, err := os.CreateTemp(root, "."+NudgeFile+".*")
	if err != nil {
		return
	}
	fmt.Fprintf(tmp, "%d %d\n", os.Getpid(), time.Now().UnixMilli())
	name := tmp.Name()
	tmp.Close()
	if os.Rename(name, filepath.Join(root, NudgeFile)) != nil {
		os.Remove(name)
	}
}

// SchemaVersion returns the database's PRAGMA user_version.
func (s *Store) SchemaVersion() (int, error) {
	return userVersion(context.Background(), s.db)
}
