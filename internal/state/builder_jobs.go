package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// A builder job has no result row: the builder's result is its submission,
// recorded as an event. It ends when its attempt is settled:
//
//   - completed, when the attempt was submitted and then accepted or replaced
//     by a repair attempt;
//   - cancelled, when the attempt is accepted or replaced without a submission
//     (an implementation waiver), or when the run stops and it is neither.
//
// ArchiveRun refuses while any job is open.

// settledBuilderJobs ends the open builder jobs of the run's settled attempts.
const settledBuilderJobs = `UPDATE jobs SET completed_ms=?,
	state=CASE (SELECT implementation_state FROM attempts WHERE id=jobs.attempt_id) WHEN 'submitted' THEN 'completed' ELSE 'cancelled' END
	WHERE kind='builder' AND state IN ('pending','running') AND attempt_id IN (
		SELECT a.id FROM attempts a JOIN tasks t ON t.run_id=a.run_id AND t.id=a.task_id
		WHERE a.run_id=? AND (a.is_current=0 OR t.accepted_attempt_id=a.id))`

// stoppedBuilderJobs cancels every builder job of the run still open.
const stoppedBuilderJobs = `UPDATE jobs SET state='cancelled', completed_ms=?
	WHERE kind='builder' AND state IN ('pending','running') AND attempt_id IN (SELECT id FROM attempts WHERE run_id=?)`

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func settleBuilderJobs(ctx context.Context, x execer, runID string, nowMS int64, stopped bool) error {
	if _, err := x.ExecContext(ctx, settledBuilderJobs, nowMS, runID); err != nil {
		return err
	}
	if stopped {
		if _, err := x.ExecContext(ctx, stoppedBuilderJobs, nowMS, runID); err != nil {
			return err
		}
	}
	return nil
}

// SettleBuilderJobs ends the run's builder jobs whose attempt is settled and,
// when stopped is true, cancels the rest. Callers pass stopped only once the
// builder is observed stopped. Event application settles jobs as it goes; this
// covers a settled stop and runs left with open jobs. It is idempotent.
func (s *Store) SettleBuilderJobs(ctx context.Context, runID string, stopped bool) error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if err := settleBuilderJobs(ctx, tx.conn, runID, s.now().UnixMilli(), stopped); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// SetBuilderJobSession records the conversation a builder job ran in, once
// known. A fresh Codex pane has no session at send time, so the controller
// reads it at submission. A session already recorded is kept.
func (s *Store) SetBuilderJobSession(ctx context.Context, jobID, session string) error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	if session == "" {
		return fmt.Errorf("%w: no session to record for job %s", workflow.ErrInvalidTransition, jobID)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET session_id=? WHERE id=? AND kind='builder' AND COALESCE(session_id,'')=''`, session, jobID)
	return err
}
