package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/workflow"
)

// GCReport is what a collection found, or did. Every list is sorted by hash.
type GCReport struct {
	Apply bool `json:"apply"`
	// Candidates are the artifacts nothing refers to: not a column of any row,
	// not an artifact_refs entry, not named inside a referenced snapshot or patch.
	Candidates []string `json:"candidates"`
	// Deleted are the candidates this call removed (empty unless Apply).
	Deleted []string `json:"deleted"`
	// Retained counts the distinct artifacts that are referenced.
	Retained int `json:"retained"`
	// Missing are referenced artifacts with no file; Corrupt are referenced
	// artifacts whose file does not hash to its name. Neither is ever a
	// candidate: they are evidence of a problem, not garbage.
	Missing []string `json:"missing"`
	Corrupt []string `json:"corrupt"`
}

var hashName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// gcDocLimit bounds how much of a referenced artifact is read to find the
// artifacts it names.
const gcDocLimit = 64 << 20

// GC inventories the artifacts nothing refers to and, with apply, deletes them.
//
// A dry run only reads. Apply takes the checkout's exclusive operation lock,
// which is refused while any publisher holds the shared one, and needs an idle
// checkout: no running or stopping run, no held writer lease. It computes the
// references after taking the lock, never from an earlier report, so an
// artifact referenced since a dry run is kept. It deletes a candidate's row
// first and its file second, so a crash leaves a file to collect next time and
// never a row that names nothing. Nothing is deleted because of its age, and no
// run is ever deleted.
func (s *Store) GC(ctx context.Context, apply bool) (GCReport, error) {
	if apply && s.readOnly {
		return GCReport{}, fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	if apply {
		locker, err := OpenLocker(s.Root())
		if err != nil {
			return GCReport{}, err
		}
		unlock, err := locker.TryLockExclusive()
		if err != nil {
			return GCReport{}, fmt.Errorf("%w: an artifact is being published or another maintenance run holds the lock", err)
		}
		defer unlock()
		if err := s.requireIdle(ctx); err != nil {
			return GCReport{}, err
		}
	}
	store, err := artifact.Open(s.Root())
	if err != nil {
		return GCReport{}, err
	}
	rep, err := s.inventory(ctx, store)
	if err != nil {
		return rep, err
	}
	rep.Apply = apply
	if !apply {
		return rep, nil
	}
	for _, h := range rep.Candidates {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM artifacts WHERE hash=?`, h); err != nil {
			if isForeignKey(err) {
				continue // referenced after all: keep it
			}
			return rep, err
		}
		p := filepath.Join(s.Root(), "artifacts", "sha256", h[:2], h)
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			if err := os.Remove(p); err != nil {
				return rep, err
			}
		}
		rep.Deleted = append(rep.Deleted, h)
	}
	if rep.Deleted == nil {
		rep.Deleted = []string{}
	}
	return rep, nil
}

func isForeignKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// requireIdle refuses while any writer may be active.
func (s *Store) requireIdle(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE state IN ('running','stopping')`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d run(s) are running or stopping; collect only when the checkout is idle", workflow.ErrBusy, n)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM checkout_leases WHERE state IN ('owned','reconciling')`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: a writer lease is held; an expired lease is not proof that its writer stopped", workflow.ErrBusy)
	}
	return nil
}

// inventory computes the report from the database and the files as they are
// now.
func (s *Store) inventory(ctx context.Context, store *artifact.Store) (GCReport, error) {
	rep := GCReport{Candidates: []string{}, Deleted: []string{}, Missing: []string{}, Corrupt: []string{}}
	referenced, err := s.referencedHashes(ctx)
	if err != nil {
		return rep, err
	}
	onDisk, err := s.filesOnDisk()
	if err != nil {
		return rep, err
	}
	// What a referenced snapshot or patch names inside its own bytes is
	// referenced too: no row says so.
	queue := make([]string, 0, len(referenced))
	for h := range referenced {
		queue = append(queue, h)
	}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if !onDisk[h] {
			continue
		}
		c, err := store.Read(ctx, h, 0, 1)
		if err != nil || c.TotalBytes > gcDocLimit {
			continue
		}
		raw, err := store.ReadAll(ctx, h, gcDocLimit)
		if err != nil {
			continue
		}
		for _, child := range snapshot.Children(raw) {
			if !referenced[child] {
				referenced[child] = true
				queue = append(queue, child)
			}
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT hash FROM artifacts`)
	if err != nil {
		return rep, err
	}
	known := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return rep, err
		}
		known[h] = true
	}
	rows.Close()

	for h := range referenced {
		switch _, err := store.Read(ctx, h, 0, 1); {
		case !onDisk[h]:
			rep.Missing = append(rep.Missing, h)
		case err != nil:
			rep.Corrupt = append(rep.Corrupt, h)
		}
	}
	all := map[string]bool{}
	for h := range onDisk {
		all[h] = true
	}
	for h := range known {
		all[h] = true
	}
	for h := range all {
		if !referenced[h] {
			rep.Candidates = append(rep.Candidates, h)
		}
	}
	rep.Retained = len(referenced)
	sort.Strings(rep.Candidates)
	sort.Strings(rep.Missing)
	sort.Strings(rep.Corrupt)
	return rep, nil
}

// referencedHashes is every hash a row names: each column that is a foreign key
// to artifacts(hash), found from the schema itself so a column added later is
// counted without anyone remembering to list it.
func (s *Store) referencedHashes(ctx context.Context) (map[string]bool, error) {
	tables, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name<>'artifacts'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			tables.Close()
			return nil, err
		}
		names = append(names, n)
	}
	tables.Close()

	out := map[string]bool{}
	for _, table := range names {
		fks, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
		if err != nil {
			return nil, err
		}
		var cols []string
		for fks.Next() {
			var id, seq int
			var parent, from, onUpdate, onDelete, match string
			var to *string
			if err := fks.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				fks.Close()
				return nil, err
			}
			if parent == "artifacts" {
				cols = append(cols, from)
			}
		}
		fks.Close()
		for _, col := range cols {
			rs, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT %q FROM %q WHERE %q IS NOT NULL`, col, table, col))
			if err != nil {
				return nil, err
			}
			for rs.Next() {
				var h string
				if err := rs.Scan(&h); err != nil {
					rs.Close()
					return nil, err
				}
				out[h] = true
			}
			rs.Close()
		}
	}
	return out, nil
}

// filesOnDisk lists the regular files that are named like artifacts. Anything
// else under the store, a symlink included, is left alone and unreported.
func (s *Store) filesOnDisk() (map[string]bool, error) {
	base := filepath.Join(s.Root(), "artifacts", "sha256")
	out := map[string]bool{}
	shards, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(base, shard.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			name := f.Name()
			if !hashName.MatchString(name) || name[:2] != shard.Name() {
				continue
			}
			if info, err := f.Info(); err == nil && info.Mode().IsRegular() {
				out[name] = true
			}
		}
	}
	return out, nil
}
