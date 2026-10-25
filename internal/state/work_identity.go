package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Logical work identity inside a collaboration. A work id is immutable; plan
// task names and run-local task ids are aliases and bindings of it. Budget
// groups carry origin repair allowances: only new origin work creates one, so
// renaming, splitting, merging or replacing a run never mints an allowance.

// ErrWorkMapping is matched by an error for a work mapping that names unknown
// work or cannot be resolved without a person: a merge of work that does not
// exist, an alias of work that was split. The caller pauses for explicit
// reconciliation; nothing is guessed.
var ErrWorkMapping = errors.New("unknown or ambiguous work mapping")

// WorkRequest registers new origin work: an immutable logical id, its own
// budget group, its first alias and, optionally, the run task it starts as.
type WorkRequest struct {
	CollaborationID, ID, BudgetGroupID, Alias string
	RunID, TaskID                             string
}

// AliasRequest names existing work again: a rename, or a replacement run's
// task (RunID and TaskID) that continues the same work.
type AliasRequest struct {
	CollaborationID, LogicalWorkID, Alias string
	RunID, TaskID                         string
}

// WorkChild is one piece of a split.
type WorkChild struct{ ID, Alias string }

// SplitRequest divides active work into two or more pieces that keep all of
// the parent's budget groups.
type SplitRequest struct {
	CollaborationID, ParentID string
	Children                  []WorkChild
}

// MergeRequest joins two or more active works into one that keeps every
// source's budget groups.
type MergeRequest struct {
	CollaborationID string
	SourceIDs       []string
	ID, Alias       string
}

// LogicalWork is one unit of work and the origin budget groups it draws on.
type LogicalWork struct {
	ID, CollaborationID, OriginName, State string
	BudgetGroups                           []string
}

func workTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, collab, id string) (LogicalWork, error) {
	w := LogicalWork{ID: id}
	err := q.QueryRowContext(ctx, `SELECT collaboration_id,origin_name,state FROM logical_work WHERE id=? AND collaboration_id=?`,
		id, collab).Scan(&w.CollaborationID, &w.OriginName, &w.State)
	if errors.Is(err, sql.ErrNoRows) {
		return w, fmt.Errorf("%w: no work %s in collaboration %s", ErrWorkMapping, id, collab)
	}
	if err != nil {
		return w, err
	}
	rows, err := q.QueryContext(ctx, `SELECT budget_group_id FROM work_budget_groups WHERE logical_work_id=? ORDER BY budget_group_id`, id)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return w, err
		}
		w.BudgetGroups = append(w.BudgetGroups, g)
	}
	return w, rows.Err()
}

// LogicalWorkByID returns one work and its budget groups.
func (s *Store) LogicalWorkByID(ctx context.Context, collaborationID, id string) (LogicalWork, error) {
	return workTx(ctx, s.db, collaborationID, id)
}

// ResolveWork follows an alias to the work it names now. Work merged into one
// successor resolves to that successor; split work has several and is an
// ErrWorkMapping that needs explicit reconciliation.
func (s *Store) ResolveWork(ctx context.Context, collaborationID, alias string) (LogicalWork, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT logical_work_id FROM work_aliases WHERE collaboration_id=? AND alias=?`,
		collaborationID, alias).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return LogicalWork{}, fmt.Errorf("%w: no alias %q in collaboration %s", ErrWorkMapping, alias, collaborationID)
	}
	if err != nil {
		return LogicalWork{}, err
	}
	for seen := map[string]bool{}; !seen[id]; {
		seen[id] = true
		w, err := workTx(ctx, s.db, collaborationID, id)
		if err != nil || w.State == "active" {
			return w, err
		}
		next, err := s.successors(ctx, id)
		if err != nil {
			return LogicalWork{}, err
		}
		if len(next) != 1 {
			return LogicalWork{}, fmt.Errorf("%w: %q names work %s, which now continues as %v; reconcile which one is meant",
				ErrWorkMapping, alias, id, next)
		}
		id = next[0]
	}
	return LogicalWork{}, fmt.Errorf("%w: the ancestry of %q loops", ErrWorkMapping, alias)
}

func (s *Store) successors(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT child_work_id FROM work_ancestry WHERE parent_work_id=? ORDER BY child_work_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateLogicalWork registers new origin work with its own budget group.
// Repeating an identical request succeeds; any other reuse of the work id,
// budget group, alias or run task is ErrConflict.
func (s *Store) CreateLogicalWork(ctx context.Context, req WorkRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if !idPattern.MatchString(req.ID) || !idPattern.MatchString(req.BudgetGroupID) || req.Alias == "" ||
		(req.RunID == "") != (req.TaskID == "") {
		return invalid("work needs 32 hex character work and budget group ids, an alias, and a run with its task or neither")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if err := requireOpen(ctx, tx, req.CollaborationID); err != nil {
		return err
	}
	if w, err := workTx(ctx, tx.conn, req.CollaborationID, req.ID); err == nil {
		if w.OriginName != req.Alias || strings.Join(w.BudgetGroups, ",") != req.BudgetGroupID {
			return fmt.Errorf("%w: work %s exists with another origin or budget group", workflow.ErrConflict, req.ID)
		}
		if err := attachTx(ctx, tx, req.CollaborationID, req.ID, req.Alias, req.RunID, req.TaskID, s.now().UnixMilli()); err != nil {
			return err
		}
		return tx.commit(ctx)
	} else if !errors.Is(err, ErrWorkMapping) {
		return err
	}
	now := s.now().UnixMilli()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO logical_work(id,collaboration_id,origin_name,state,created_ms) VALUES(?,?,?,'active',?)`,
			[]any{req.ID, req.CollaborationID, req.Alias, now}},
		{`INSERT INTO budget_groups(id,collaboration_id,created_ms) VALUES(?,?,?)`, []any{req.BudgetGroupID, req.CollaborationID, now}},
		{`INSERT INTO work_budget_groups(logical_work_id,budget_group_id) VALUES(?,?)`, []any{req.ID, req.BudgetGroupID}},
	} {
		if _, err := tx.conn.ExecContext(ctx, q.sql, q.args...); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "PRIMARY KEY") {
				return fmt.Errorf("%w: %v", workflow.ErrConflict, err)
			}
			return err
		}
	}
	if err := attachTx(ctx, tx, req.CollaborationID, req.ID, req.Alias, req.RunID, req.TaskID, now); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// AttachWorkAlias names active work again, and binds a replacement run's task
// to it when RunID and TaskID are given. The work keeps its id and groups.
func (s *Store) AttachWorkAlias(ctx context.Context, req AliasRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if req.Alias == "" || (req.RunID == "") != (req.TaskID == "") {
		return invalid("an alias is named, with a run and its task or neither")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if err := requireOpen(ctx, tx, req.CollaborationID); err != nil {
		return err
	}
	w, err := workTx(ctx, tx.conn, req.CollaborationID, req.LogicalWorkID)
	if err != nil {
		return err
	}
	if w.State != "active" {
		return fmt.Errorf("%w: work %s was %s; name its successor instead", ErrWorkMapping, w.ID, w.State)
	}
	if err := attachTx(ctx, tx, req.CollaborationID, req.LogicalWorkID, req.Alias, req.RunID, req.TaskID, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// attachTx adds an alias and an optional run task binding for work, accepting
// ones that already name the same work and refusing ones that name other work.
func attachTx(ctx context.Context, tx *dbTx, collab, workID, alias, runID, taskID string, now int64) error {
	var current string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT logical_work_id FROM work_aliases WHERE collaboration_id=? AND alias=?`,
		collab, alias).Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO work_aliases(collaboration_id,alias,logical_work_id,created_ms) VALUES(?,?,?,?)`,
			collab, alias, workID, now); err != nil {
			return err
		}
	case err != nil:
		return err
	case current != workID:
		return fmt.Errorf("%w: alias %q already names work %s", workflow.ErrConflict, alias, current)
	}
	if runID == "" {
		return nil
	}
	switch err := tx.conn.QueryRowContext(ctx, `SELECT logical_work_id FROM work_task_bindings WHERE run_id=? AND task_id=?`,
		runID, taskID).Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO work_task_bindings(run_id,task_id,logical_work_id) VALUES(?,?,?)`,
			runID, taskID, workID); err != nil {
			if isForeignKey(err) {
				return invalid("run %s has no task %s", runID, taskID)
			}
			return err
		}
	case err != nil:
		return err
	case current != workID:
		return fmt.Errorf("%w: task %s of run %s already belongs to work %s", workflow.ErrConflict, taskID, runID, current)
	}
	return nil
}

// WorkAliasExists reports whether an alias already names work, whether or not
// it still resolves to one piece of active work.
func (s *Store) WorkAliasExists(ctx context.Context, collaborationID, alias string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM work_aliases WHERE collaboration_id=? AND alias=?`, collaborationID, alias).Scan(&n)
	return n > 0, err
}
