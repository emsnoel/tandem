package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// SettleDeliveryNotRead ends an open delivery on a person's statement that the
// agent never read the prompt. Only the agent's nonce proves a prompt was read,
// and this is the one other way to close one: it does not treat the prompt as
// read, and nothing types it again. The effect is cancelled, the job waiting on
// it is ended, and who said so and why are kept as a manual intervention.
//
// It refuses unless the run is paused or stopping, the effect is this run's and
// its delivery is open, and the writer lease is free. A held lease means a stop
// has not yet seen the writer quiet, and an expired one is not proof it stopped.
func (s *Store) SettleDeliveryNotRead(ctx context.Context, runID, effectID, reasonHash, actor string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if !hashPattern.MatchString(reasonHash) || actor == "" {
		return invalid("settling a delivery names who said so and why")
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	conflict := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{workflow.ErrConflict}, a...)...)
	}
	var runState, checkout string
	err = tx.conn.QueryRowContext(ctx, `SELECT r.state, p.checkout_id FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`, runID).Scan(&runState, &checkout)
	if errors.Is(err, sql.ErrNoRows) {
		return conflict("there is no run %s", runID)
	}
	if err != nil {
		return err
	}
	if runState != "paused" && runState != "stopping" {
		return conflict("run %s is %s, and only a paused or stopping run settles a delivery; run tandem build stop first", runID, runState)
	}
	var kind, effState string
	var attempt, job, task sql.NullString
	err = tx.conn.QueryRowContext(ctx, `SELECT e.kind,e.state,e.attempt_id,e.job_id,a.task_id FROM effects e LEFT JOIN attempts a ON a.id=e.attempt_id
		WHERE e.id=? AND e.run_id=?`, effectID, runID).Scan(&kind, &effState, &attempt, &job, &task)
	if errors.Is(err, sql.ErrNoRows) {
		return conflict("run %s has no effect %s", runID, effectID)
	}
	if err != nil {
		return err
	}
	if effState != "delivering" && effState != "delivery_unknown" {
		return conflict("effect %s is %s, so its delivery is not open", effectID, effState)
	}
	var held int
	if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND state IN ('owned','reconciling')`, checkout).Scan(&held); err != nil {
		return err
	}
	if held > 0 {
		return fmt.Errorf("%w: the checkout's writer lease is held; run tandem build stop and wait until the run is paused, then settle again", workflow.ErrBusy)
	}
	now := s.now().UnixMilli()
	if err := endDeliveryJob(ctx, tx, kind, attempt.String, job.String, now); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='cancelled', lease_owner=NULL, lease_until_ms=NULL WHERE id=?`, effectID); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO manual_interventions(id,run_id,task_id,effect_id,actor,reason_hash,created_ms) VALUES(?,?,?,?,?,?,?)`,
		id, runID, nullable(task.String), effectID, actor, reasonHash, now); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// endDeliveryJob ends the job that waits on a prompt: the job it is bound to, or
// without one the attempt's builder job or review job. A notice has no job.
func endDeliveryJob(ctx context.Context, tx *dbTx, kind, attempt, job string, now int64) error {
	const open = `state IN ('pending','running','cancelling','unknown')`
	var err error
	switch {
	case job != "":
		_, err = tx.conn.ExecContext(ctx, `UPDATE jobs SET state='cancelled', completed_ms=? WHERE id=? AND `+open, now, job)
	case attempt == "":
	case kind == workflow.EffectBuilderDispatch:
		_, err = tx.conn.ExecContext(ctx, `UPDATE jobs SET state='cancelled', completed_ms=? WHERE attempt_id=? AND kind='builder' AND `+open, now, attempt)
	case kind == "review_prompt":
		_, err = tx.conn.ExecContext(ctx, `UPDATE jobs SET state='cancelled', completed_ms=? WHERE attempt_id=? AND kind='reviewer' AND `+open+`
			AND NOT EXISTS (SELECT 1 FROM review_stages s WHERE s.job_id=jobs.id AND s.stage='independent_verification')`, now, attempt)
	}
	return err
}

// EndStoppedRunJobs cancels what a stopped run left open: a check that never
// finished, or a review or verification whose prompt is not open. Nothing else
// can end those, and a stopped run takes no result. A job whose prompt's delivery
// is still open stays: it ends with that delivery.
func (s *Store) EndStoppedRunJobs(ctx context.Context, runID string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='cancelled', completed_ms=?
		WHERE state IN ('pending','running','cancelling','unknown') AND attempt_id IN (SELECT id FROM attempts WHERE run_id=?)
		AND NOT EXISTS (SELECT 1 FROM effects e WHERE e.attempt_id=jobs.attempt_id AND e.state IN ('delivering','delivery_unknown')
			AND (e.job_id=jobs.id OR (e.job_id IS NULL AND e.kind=CASE jobs.kind WHEN 'builder' THEN 'builder_dispatch' WHEN 'reviewer' THEN 'review_prompt' END)))`,
		s.now().UnixMilli(), runID)
	return err
}
