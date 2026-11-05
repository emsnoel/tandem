package state

// Manual reconcile: a person edited the tree outside the run and hands it back.
//
// The failed attempt is superseded by a reconcile attempt that starts from the
// task's original tree, so checks and both review stages run again on the whole
// task as it is now, edits included. The edit is recorded as an intervention.
// No repair allowance is consumed, created or reset: a reconcile is not an
// automatic repair, and the attempts it adds are bounded by MaxAttempts.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// ReconcileRequest hands a run an outside edit. ID is the idempotency key: one
// per failed attempt.
type ReconcileRequest struct {
	ID, RunID, TaskID, FailedAttemptID string
	FailedVersion                      int64
	Actor, ReasonHash, TreeHash        string
	RunVersion, LeaseFence             int64
	Attempt                            AttemptSpec
}

// Reconciliation is the attempt a reconcile made.
type Reconciliation struct {
	ID, AttemptID string
	Duplicate     bool
}

// reconcilable pause codes: the failures a person can edit their way out of,
// and the run waiting on a decision about one.
var reconcilable = map[string]bool{workflow.PauseStuck: true, workflow.PauseCheckFailed: true, workflow.PauseReviewFailed: true, "recovery_blocked": true}

// ReconcileAttempt supersedes a failed attempt with a reconcile attempt, once.
func (s *Store) ReconcileAttempt(ctx context.Context, req ReconcileRequest) (Reconciliation, error) {
	if err := s.writable(); err != nil {
		return Reconciliation{}, err
	}
	for name, id := range map[string]string{"request": req.ID, "run": req.RunID, "failed attempt": req.FailedAttemptID} {
		if !idPattern.MatchString(id) {
			return Reconciliation{}, invalid("the %s id must be 32 lowercase hex characters", name)
		}
	}
	if req.TaskID == "" || req.Actor == "" || !hashPattern.MatchString(req.ReasonHash) || !hashPattern.MatchString(req.TreeHash) {
		return Reconciliation{}, invalid("a reconcile names its task, who made it, why, and the tree it hands over")
	}
	if err := validateAttemptSpec(req.Attempt); err != nil {
		return Reconciliation{}, err
	}
	if req.Attempt.RunID != req.RunID || req.Attempt.TaskID != req.TaskID || req.Attempt.ParentID != req.FailedAttemptID {
		return Reconciliation{}, invalid("the reconcile attempt must belong to the same run and task and name the failed attempt as its parent")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Reconciliation{}, err
	}
	defer tx.rollback()
	var prior Reconciliation
	var priorFrom string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT id,to_attempt_id,from_attempt_id FROM manual_interventions WHERE id=? OR from_attempt_id=?`,
		req.ID, req.FailedAttemptID).Scan(&prior.ID, &prior.AttemptID, &priorFrom); {
	case err == nil:
		if prior.ID != req.ID || priorFrom != req.FailedAttemptID || prior.AttemptID != req.Attempt.ID {
			return Reconciliation{}, fmt.Errorf("%w: attempt %s was already reconciled by another request", workflow.ErrConflict, req.FailedAttemptID)
		}
		prior.Duplicate = true
		return prior, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Reconciliation{}, err
	}
	var runState, checkout string
	var pause sql.NullString
	var version int64
	if err := tx.conn.QueryRowContext(ctx, `SELECT r.state,r.pause_code,r.version,p.checkout_id FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`,
		req.RunID).Scan(&runState, &pause, &version, &checkout); errors.Is(err, sql.ErrNoRows) {
		return Reconciliation{}, invalid("no run %s", req.RunID)
	} else if err != nil {
		return Reconciliation{}, err
	}
	if runState != "paused" || !reconcilable[pause.String] {
		return Reconciliation{}, invalid("run %s is %s (%s); only a run paused by a failure can be reconciled", req.RunID, runState, pause.String)
	}
	if version != req.RunVersion {
		return Reconciliation{}, fmt.Errorf("%w: run %s is at version %d, not %d", workflow.ErrConflict, req.RunID, version, req.RunVersion)
	}
	var owned int
	if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND state='owned' AND fence=? AND expires_ms>?`,
		checkout, req.LeaseFence, s.now().UnixMilli()).Scan(&owned); err != nil {
		return Reconciliation{}, err
	}
	if owned != 1 {
		return Reconciliation{}, fmt.Errorf("%w: the writer lease of checkout %s is not held at fence %d", workflow.ErrBusy, checkout, req.LeaseFence)
	}
	var accepted sql.NullString
	if err := tx.conn.QueryRowContext(ctx, `SELECT accepted_attempt_id FROM tasks WHERE run_id=? AND id=?`, req.RunID, req.TaskID).Scan(&accepted); err != nil {
		return Reconciliation{}, invalid("run %s has no task %s", req.RunID, req.TaskID)
	}
	if accepted.Valid {
		return Reconciliation{}, fmt.Errorf("%w: task %s is already accepted", workflow.ErrConflict, req.TaskID)
	}
	if req.Attempt.Ordinal > MaxAttempts {
		return Reconciliation{}, fmt.Errorf("%w: task %s has used its %d attempts", workflow.ErrConflict, req.TaskID, MaxAttempts)
	}
	res, err := tx.conn.ExecContext(ctx, `UPDATE attempts SET is_current=0, version=version+1 WHERE id=? AND run_id=? AND is_current=1 AND version=?`,
		req.FailedAttemptID, req.RunID, req.FailedVersion)
	if err != nil {
		return Reconciliation{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Reconciliation{}, fmt.Errorf("%w: attempt %s is not the current attempt at version %d", workflow.ErrStaleAttempt, req.FailedAttemptID, req.FailedVersion)
	}
	if err := s.createAttemptTx(ctx, tx, req.Attempt); err != nil {
		return Reconciliation{}, err
	}
	now := s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO manual_interventions(id,run_id,task_id,from_attempt_id,to_attempt_id,actor,reason_hash,tree_hash,created_ms)
		VALUES(?,?,?,?,?,?,?,?,?)`, req.ID, req.RunID, req.TaskID, req.FailedAttemptID, req.Attempt.ID, req.Actor, req.ReasonHash, req.TreeHash, now); err != nil {
		return Reconciliation{}, err
	}
	// The failure these incidents were about has been handed to a person.
	if _, err := tx.conn.ExecContext(ctx, `UPDATE recovery_incidents SET state='resolved', resolution='reconciled', resolved_ms=? WHERE attempt_id=? AND state<>'resolved'`,
		now, req.FailedAttemptID); err != nil {
		return Reconciliation{}, err
	}
	if err := tx.commit(ctx); err != nil {
		return Reconciliation{}, err
	}
	return Reconciliation{ID: req.ID, AttemptID: req.Attempt.ID}, nil
}

// ManualInterventions counts a run's reconciles, for the evaluation's
// intervention figure.
func (s *Store) ManualInterventions(ctx context.Context, runID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM manual_interventions WHERE run_id=?`, runID).Scan(&n)
	return n, err
}

// ReconcileInto reports whether an attempt was made by a reconcile, and the
// attempt it replaced. A reconcile attempt that never reached its submission is
// finished by the controller on its next pass.
func (s *Store) ReconcileInto(ctx context.Context, attemptID string) (from string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT from_attempt_id FROM manual_interventions WHERE to_attempt_id=?`, attemptID).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return from, err == nil, err
}
