package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var embedded embed.FS

// migration is one ordered schema step. Its version is the PRAGMA
// user_version the database has once it has been applied.
type migration struct {
	version int
	name    string
	sql     string
}

// testHooks injects failures at named boundaries. Production passes the zero
// value; only tests set a field.
type testHooks struct {
	// beforeCommit runs after a migration's statements and its user_version
	// update, immediately before COMMIT. Returning an error rolls back.
	beforeCommit func(version int) error
}

var migrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

// loadMigrations reads migrations/NNN_name.sql files. Versions must start at 1
// and be contiguous, so a missing file is an error rather than a silently
// skipped schema step.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q is not named NNN_name.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1: %s is at position %d", m.name, i+1)
		}
	}
	return out, nil
}

// latestSchema is the schema version this binary supports.
var latestSchema = func() int {
	ms, err := loadMigrations(embedded)
	if err != nil {
		panic("state: embedded migrations: " + err.Error())
	}
	return len(ms)
}()

// LatestSchema returns the schema version this binary supports.
func LatestSchema() int { return latestSchema }

// ErrNewerSchema is matched by the error returned for a database written by a
// newer tandem. Use errors.As with *NewerSchemaError for the versions.
var ErrNewerSchema = errors.New("state schema is newer than this tandem supports")

// NewerSchemaError carries the diagnostic for a store this binary must not
// touch: it cannot know the layout of tables a later version added.
type NewerSchemaError struct{ Found, Supported int }

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("state schema is version %d but this tandem supports up to %d; upgrade tandem (nothing was changed)", e.Found, e.Supported)
}

func (e *NewerSchemaError) Is(target error) bool { return target == ErrNewerSchema }

func userVersion(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var v int
	err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	return v, err
}

// migrate brings the database to the last version in ms. Each step runs in its
// own IMMEDIATE transaction on one reserved connection, with the version bump
// inside it, so a failure leaves the previous version and tables. An existing
// store is first copied with VACUUM INTO, which reads through the live WAL, and
// that backup is kept if anything fails.
func migrate(ctx context.Context, db *sql.DB, root string, ms []migration, h testHooks) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	current, err := userVersion(ctx, conn)
	if err != nil {
		return err
	}
	latest := len(ms)
	if current > latest {
		return &NewerSchemaError{Found: current, Supported: latest}
	}
	backedUp := false
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if current > 0 && !backedUp {
			if err := backup(ctx, conn, root, current); err != nil {
				return fmt.Errorf("backing up schema version %d before migrating: %w", current, err)
			}
			backedUp = true
		}
		if err := apply(ctx, conn, m, h); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func apply(ctx context.Context, conn *sql.Conn, m migration, h testHooks) (err error) {
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Not ctx: a cancelled context must not strand an open transaction.
			if _, rbErr := conn.ExecContext(context.Background(), `ROLLBACK`); rbErr != nil {
				err = errors.Join(err, rbErr)
			}
		}
	}()
	if _, err = conn.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	// PRAGMA takes no bound parameter; the version is a parsed int.
	if _, err = conn.ExecContext(ctx, `PRAGMA user_version = `+strconv.Itoa(m.version)); err != nil {
		return err
	}
	if h.beforeCommit != nil {
		if err = h.beforeCommit(m.version); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

// backup writes a consistent copy of the database to <root>/backups.
func backup(ctx context.Context, conn *sql.Conn, root string, version int) error {
	dir := filepath.Join(root, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("state-v%d-%s.sqlite", version, hex.EncodeToString(suffix)))
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	// A backup that cannot be read back is not a backup.
	check, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer check.Close()
	got, err := userVersion(ctx, check)
	if err != nil {
		return fmt.Errorf("verifying backup %s: %w", path, err)
	}
	if got != version {
		return fmt.Errorf("backup %s is at version %d, want %d", path, got, version)
	}
	return nil
}
