package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// ArchiveRun marks a finished or paused run as history. Nothing is deleted:
// its rows and artifacts stay, and an archived run is never driven again. Its
// effects that never started are cancelled.
//
// It refuses while the run could still be acting: running or stopping, a job
// or delivery whose outcome is open, or a held writer lease (an expired one
// included). Archiving an archived run is a no-op.
func (s *Store) ArchiveRun(ctx context.Context, runID string) error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var st, checkout string
	err = tx.conn.QueryRowContext(ctx, `SELECT r.state, p.checkout_id FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`, runID).Scan(&st, &checkout)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: there is no run %s", workflow.ErrConflict, runID)
	}
	if err != nil {
		return err
	}
	if st == "archived" {
		return nil
	}
	busy := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{workflow.ErrBusy}, a...)...)
	}
	if st == "running" || st == "stopping" {
		return busy("run %s is %s; stop it and let its builder settle first", runID, st)
	}
	count := func(q string, args ...any) (n int, err error) {
		err = tx.conn.QueryRowContext(ctx, q, args...).Scan(&n)
		return
	}
	if n, err := count(`SELECT count(*) FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=? AND j.state IN ('pending','running','cancelling','unknown')`, runID); err != nil {
		return err
	} else if n > 0 {
		return busy("run %s has %d job(s) whose outcome is open; end them with tandem state recover --run %s, then archive again", runID, n, runID)
	}
	if n, err := count(`SELECT count(*) FROM effects WHERE run_id=? AND state IN ('delivering','delivery_unknown')`, runID); err != nil {
		return err
	} else if n > 0 {
		return busy("run %s has %d delivery whose outcome is open", runID, n)
	}
	if n, err := count(`SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND state IN ('owned','reconciling')`, checkout); err != nil {
		return err
	} else if n > 0 {
		return busy("the checkout's writer lease is held; an expired lease is not proof that its writer stopped")
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE runs SET state='archived', archived_ms=?, version=version+1 WHERE id=?`, s.now().UnixMilli(), runID); err != nil {
		return err
	}
	// What the run never started it never will: cancel it, so no later run in
	// this checkout claims it. Deliveries whose outcome is open refused the
	// archive above, so only pending effects are left.
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='cancelled' WHERE run_id=? AND state='pending'`, runID); err != nil {
		return err
	}
	return tx.commit(ctx)
}
