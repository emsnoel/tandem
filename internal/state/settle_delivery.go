package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// EventDeliveryAbandoned is an owner giving up on a prompt. It is in the run's
// log and export, and it is no workflow event: nothing reduces it, no builder
// can send it, and it moves no version.
const EventDeliveryAbandoned = "delivery_abandoned"

// AbandonDelivery ends an open delivery on a person's statement that they are
// giving up on the prompt. Only the agent's nonce shows a prompt was read, and
// this is the one other way to close one: it does not treat the prompt as read,
// and nothing types it again. The effect is cancelled, the job waiting on it is
// ended, and payloadHash, the stored statement of who and why, is logged as an
// event. If no session was bound nothing can have been spent, so the reservation
// goes back and the source ends. A bound session's usage may be real, so that
// stays as it is.
//
// It refuses unless the run is paused or stopping, the effect is this run's and
// its delivery is open, and the writer lease is free. A held lease means a stop
// has not yet seen the writer quiet, and an expired one is not proof it stopped.
func (s *Store) AbandonDelivery(ctx context.Context, runID, effectID, payloadHash string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if !hashPattern.MatchString(payloadHash) {
		return invalid("abandoning a delivery stores who said so and why")
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
	var version int64
	err = tx.conn.QueryRowContext(ctx, `SELECT r.state, r.version, p.checkout_id FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`, runID).Scan(&runState, &version, &checkout)
	if errors.Is(err, sql.ErrNoRows) {
		return conflict("there is no run %s", runID)
	}
	if err != nil {
		return err
	}
	if runState != "paused" && runState != "stopping" {
		return conflict("run %s is %s, and only a paused or stopping run abandons a delivery; run tandem build stop first", runID, runState)
	}
	var kind, effState string
	var attempt, job sql.NullString
	err = tx.conn.QueryRowContext(ctx, `SELECT kind,state,attempt_id,job_id FROM effects WHERE id=? AND run_id=?`, effectID, runID).Scan(&kind, &effState, &attempt, &job)
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
		return fmt.Errorf("%w: the checkout's writer lease is held; run tandem build stop and wait until the run is paused, then try again. If the builder's pane is gone the stop cannot finish, so bring the agent back with tandem-pair first", workflow.ErrBusy)
	}
	now := s.now().UnixMilli()
	if err := endDeliveryJob(ctx, tx, kind, attempt.String, job.String, now); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='cancelled', lease_owner=NULL, lease_until_ms=NULL WHERE id=?`, effectID); err != nil {
		return err
	}
	if err := releaseUnboundLaunch(ctx, tx, effectID, now); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO events(id,run_id,attempt_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?,?,?,?,?,?,?)`,
		id, runID, nullable(attempt.String), EventDeliveryAbandoned, payloadHash, version, version, now); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('event',?,'payload',?)`, id, payloadHash); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// releaseUnboundLaunch gives back what a launch with no session holds: its
// reservation, and the source registered for it, which can never resolve. A
// shared source, as a planner's is, stays. It only started no session here.
func releaseUnboundLaunch(ctx context.Context, tx *dbTx, effectID string, now int64) error {
	var unbound int
	err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_admissions a JOIN expected_sources s ON s.id=a.source_id
		WHERE a.effect_id=? AND COALESCE(s.session_id,'')=''`, effectID).Scan(&unbound)
	if err != nil {
		return err
	}
	if unbound > 0 {
		if err := releaseEffectAdmissions(ctx, tx, now, effectID); err != nil {
			return err
		}
	}
	_, err = tx.conn.ExecContext(ctx, `UPDATE expected_sources SET status='abandoned' WHERE start_boundary=? AND status='expected' AND COALESCE(session_id,'')=''`, effectID)
	return err
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
