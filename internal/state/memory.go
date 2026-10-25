package state

// Persistence for scoped memory: metadata, hash references and relations. The
// full value and scope text are artifacts and are never copied into SQL; the
// controller loads them when it needs them.
//
// Nothing here deletes or rewrites history. An entry replaced by a newer one is
// marked superseded, an entry whose binding no longer holds is marked invalid,
// and both stay with their hashes so a past decision can still be explained.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/workflow"
)

// maxSupersessionChain bounds the walk down a supersession chain, so a corrupted
// chain that loops through rows this walk has not seen cannot spin.
const maxSupersessionChain = 10000

// PutMemory records an entry. In one transaction it checks the run, the task and
// every dependency exist, that each hash names a registered artifact, and, when
// the entry supersedes another, that the replacement is allowed (same kind and
// scope, at least the same trust, target still active) and that the chain
// behind it has no cycle; then it stores the entry and marks the target
// superseded. Recording the same entry again changes nothing; the same id with
// different content is a conflict.
func (s *Store) PutMemory(ctx context.Context, e memory.Entry) error {
	if err := memory.ValidateNew(e); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()

	if prior, found, err := loadMemory(ctx, tx.conn, e.ID); err != nil {
		return err
	} else if found {
		if sameMemory(prior, e) {
			return nil
		}
		return fmt.Errorf("%w: memory entry %s already exists with different content", workflow.ErrConflict, e.ID)
	}

	var one int
	switch err := tx.conn.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id=?`, e.RunID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: run %s does not exist", workflow.ErrConflict, e.RunID)
	case err != nil:
		return err
	}
	for _, task := range append([]string{e.TaskID}, e.Dependencies...) {
		switch err := tx.conn.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE run_id=? AND id=?`, e.RunID, task).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: run %s has no task %s", workflow.ErrConflict, e.RunID, task)
		case err != nil:
			return err
		}
	}
	for role, h := range map[string]string{"scope": e.ScopeHash, "source": e.SourceHash, "validity": e.ValidityHash, "value": e.ValueHash} {
		switch err := tx.conn.QueryRowContext(ctx, `SELECT 1 FROM artifacts WHERE hash=?`, h).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: the %s artifact %s of memory entry %s is not registered", workflow.ErrIncompleteEvidence, role, h, e.ID)
		case err != nil:
			return err
		}
	}

	if e.SupersedesID != "" {
		old, found, err := loadMemory(ctx, tx.conn, e.SupersedesID)
		if err != nil {
			return err
		}
		if !found || old.RunID != e.RunID {
			return fmt.Errorf("%w: memory entry %s does not exist in run %s", workflow.ErrConflict, e.SupersedesID, e.RunID)
		}
		if err := checkChain(ctx, tx.conn, e.ID, old); err != nil {
			return err
		}
		if err := memory.CanSupersede(old, e); err != nil {
			return err
		}
		if _, err := tx.conn.ExecContext(ctx, `UPDATE memory_entries SET state='superseded' WHERE id=?`, old.ID); err != nil {
			return err
		}
	}

	required := 0
	if e.Required {
		required = 1
	}
	var supersedes any
	if e.SupersedesID != "" {
		supersedes = e.SupersedesID
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO memory_entries(id,run_id,task_id,kind,scope_hash,source_hash,validity_hash,value_hash,supersedes_id,trust,state,required)
		VALUES(?,?,?,?,?,?,?,?,?,?,'active',?)`, e.ID, e.RunID, e.TaskID, e.Kind, e.ScopeHash, e.SourceHash, e.ValidityHash, e.ValueHash, supersedes, e.Trust, required); err != nil {
		return err
	}
	for _, dep := range e.Dependencies {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO memory_dependencies(memory_id,run_id,task_id) VALUES(?,?,?)`, e.ID, e.RunID, dep); err != nil {
			return err
		}
	}
	for role, h := range map[string]string{"scope": e.ScopeHash, "source": e.SourceHash, "validity": e.ValidityHash, "value": e.ValueHash} {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('memory',?,?,?)`, e.ID, role, h); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

// checkChain walks the supersession chain from target back through the entries
// each one replaced and rejects a loop, or one that returns to the new entry.
func checkChain(ctx context.Context, conn *sql.Conn, newID string, target memory.Entry) error {
	seen := map[string]bool{newID: true}
	cur := target
	for i := 0; i < maxSupersessionChain; i++ {
		if seen[cur.ID] {
			return fmt.Errorf("%w: supersession cycle through memory entry %s", workflow.ErrInvalidTransition, cur.ID)
		}
		seen[cur.ID] = true
		if cur.SupersedesID == "" {
			return nil
		}
		next, found, err := loadMemory(ctx, conn, cur.SupersedesID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		cur = next
	}
	return fmt.Errorf("%w: supersession chain behind memory entry %s is longer than %d", workflow.ErrInvalidTransition, target.ID, maxSupersessionChain)
}

type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

const memoryColumns = `id,run_id,task_id,kind,scope_hash,source_hash,validity_hash,value_hash,COALESCE(supersedes_id,''),trust,state,required`

func scanMemory(row interface{ Scan(...any) error }) (memory.Entry, error) {
	var e memory.Entry
	var required int
	err := row.Scan(&e.ID, &e.RunID, &e.TaskID, &e.Kind, &e.ScopeHash, &e.SourceHash, &e.ValidityHash, &e.ValueHash, &e.SupersedesID, &e.Trust, &e.State, &required)
	e.Required = required == 1
	return e, err
}

func memoryDependencies(ctx context.Context, q queryer, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT task_id FROM memory_dependencies WHERE memory_id=? ORDER BY task_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadMemory(ctx context.Context, q queryer, id string) (memory.Entry, bool, error) {
	e, err := scanMemory(q.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memory_entries WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Entry{}, false, nil
	}
	if err != nil {
		return memory.Entry{}, false, err
	}
	if e.Dependencies, err = memoryDependencies(ctx, q, id); err != nil {
		return memory.Entry{}, false, err
	}
	return e, true, nil
}

// sameMemory compares what an entry is, not what has since happened to it: its
// state may have moved on.
func sameMemory(stored, e memory.Entry) bool {
	if stored.RunID != e.RunID || stored.TaskID != e.TaskID || stored.Kind != e.Kind || stored.Trust != e.Trust || stored.Required != e.Required ||
		stored.ScopeHash != e.ScopeHash || stored.SourceHash != e.SourceHash || stored.ValidityHash != e.ValidityHash || stored.ValueHash != e.ValueHash ||
		stored.SupersedesID != e.SupersedesID || len(stored.Dependencies) != len(e.Dependencies) {
		return false
	}
	want := append([]string{}, e.Dependencies...)
	sort.Strings(want)
	for i, d := range stored.Dependencies {
		if d != want[i] {
			return false
		}
	}
	return true
}

// GetMemory returns one entry's metadata. Its Value and Scope text are artifacts.
func (s *Store) GetMemory(ctx context.Context, id string) (memory.Entry, bool, error) {
	return loadMemory(ctx, s.db, id)
}

// ListMemory returns every entry of a run, active or not, ordered by id.
func (s *Store) ListMemory(ctx context.Context, runID string) ([]memory.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_entries WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	var out []memory.Entry
	for rows.Next() {
		e, err := scanMemory(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Dependencies, err = memoryDependencies(ctx, s.db, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// InvalidateMemory marks invalid every active entry of the run whose validity
// hash is not in valid, and returns their ids. The entry, its hashes and its
// place in any supersession chain stay as they were; it is simply no longer
// selected. An entry that is already superseded or invalid is history and is
// left alone.
func (s *Store) InvalidateMemory(ctx context.Context, runID string, valid map[string]bool) ([]string, error) {
	if valid == nil {
		return nil, fmt.Errorf("%w: invalidating memory needs the set of current validity hashes", workflow.ErrInvalidTransition)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.rollback()
	rows, err := tx.conn.QueryContext(ctx, `SELECT id,validity_hash FROM memory_entries WHERE run_id=? AND state='active' ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	var stale []string
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return nil, err
		}
		if !valid[v] {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range stale {
		if _, err := tx.conn.ExecContext(ctx, `UPDATE memory_entries SET state='invalid' WHERE id=? AND state='active'`, id); err != nil {
			return nil, err
		}
	}
	return stale, tx.commit(ctx)
}
