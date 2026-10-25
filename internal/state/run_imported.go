package state

import (
	"context"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// insertImported records the legacy-finished tasks of a run being created. The
// attempt is submitted and accepted, and its check and review stay pending with
// no snapshot, patch or certificate behind them: nothing is reconstructed.
func insertImported(ctx context.Context, tx *dbTx, spec RunSpec, now int64) error {
	for _, imp := range spec.Imported {
		if !idPattern.MatchString(imp.AttemptID) {
			return fmt.Errorf("%w: imported attempt ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
		}
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,
			version,is_current,accepted_ms,created_ms) VALUES(?,?,?,1,'submitted','pending','pending','unknown',0,1,?,?)`,
			imp.AttemptID, spec.RunID, imp.TaskID, now, now); err != nil {
			return fmt.Errorf("imported task %s: %w", imp.TaskID, err)
		}
		res, err := tx.conn.ExecContext(ctx, `UPDATE tasks SET accepted_attempt_id=? WHERE run_id=? AND id=?`, imp.AttemptID, spec.RunID, imp.TaskID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: imported task %s is not a task of the run", workflow.ErrInvalidTransition, imp.TaskID)
		}
		for _, r := range imp.Refs {
			if r[0] == "" || !hashPattern.MatchString(r[1]) {
				return fmt.Errorf("%w: an imported reference needs a role and a 64 lowercase hex hash", workflow.ErrInvalidTransition)
			}
			if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('attempt',?,?,?)`, imp.AttemptID, r[0], r[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// CountRuns counts every run of the store, archived history included.
func (s *Store) CountRuns(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runs`).Scan(&n)
	return n, err
}
