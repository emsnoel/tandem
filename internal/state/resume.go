package state

// A resumed run carries the tasks an earlier run of the same plan accepted. Its
// task rows name the earlier run's accepted attempts, so the check, review and
// waivers that accepted them are the same rows, read in place. Nothing is
// copied, and the earlier run stays archived history.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// LatestArchivedRun is the most recently archived run of the plan with this
// hash, or false when the store holds none.
func (s *Store) LatestArchivedRun(ctx context.Context, planHash string) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE state='archived' AND plan_hash=?
		ORDER BY archived_ms DESC, created_ms DESC, id DESC LIMIT 1`, planHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// checkCarried refuses a task row that names an accepted attempt unless that
// attempt was accepted for the same task, in an archived run of the same plan.
// It runs inside the transaction that creates the run.
func checkCarried(ctx context.Context, tx *dbTx, spec RunSpec) error {
	for _, t := range spec.Tasks {
		if t.AcceptedAttemptID == "" {
			continue
		}
		var task, plan, st string
		var accepted sql.NullInt64
		err := tx.conn.QueryRowContext(ctx, `SELECT a.task_id, a.accepted_ms, r.plan_hash, r.state FROM attempts a JOIN runs r ON r.id=a.run_id WHERE a.id=?`,
			t.AcceptedAttemptID).Scan(&task, &accepted, &plan, &st)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: task %s carries attempt %s, which does not exist", workflow.ErrConflict, t.ID, t.AcceptedAttemptID)
		case err != nil:
			return err
		case task != t.ID || !accepted.Valid:
			return fmt.Errorf("%w: task %s carries attempt %s, which was not accepted for it", workflow.ErrConflict, t.ID, t.AcceptedAttemptID)
		case plan != spec.PlanHash || st != "archived":
			return fmt.Errorf("%w: task %s carries attempt %s, which belongs to a run that is %s or of another plan", workflow.ErrConflict, t.ID, t.AcceptedAttemptID, st)
		}
	}
	return nil
}
