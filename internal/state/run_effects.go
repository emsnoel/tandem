package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// InsertDerivedEffect adds an effect that grows out of another effect's work,
// sharing its source event, with the next ordinal. Deriving the same kind from
// the same parent again returns the effect already derived, so a retry adds
// nothing.
func (s *Store) InsertDerivedEffect(ctx context.Context, parentID, kind, payloadHash string) (workflow.Effect, error) {
	if !hashPattern.MatchString(payloadHash) || kind == "" {
		return workflow.Effect{}, fmt.Errorf("%w: a derived effect needs a kind and a payload hash", workflow.ErrInvalidTransition)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return workflow.Effect{}, err
	}
	defer tx.rollback()
	var run, source string
	var attempt sql.NullString
	switch e := tx.conn.QueryRowContext(ctx, `SELECT run_id,attempt_id,source_event_id FROM effects WHERE id=?`, parentID).Scan(&run, &attempt, &source); {
	case errors.Is(e, sql.ErrNoRows):
		return workflow.Effect{}, fmt.Errorf("%w: effect %s does not exist", workflow.ErrConflict, parentID)
	case e != nil:
		return workflow.Effect{}, e
	}
	var existing string
	switch e := tx.conn.QueryRowContext(ctx, `SELECT id FROM effects WHERE source_event_id=? AND kind=? AND id<>?`, source, kind, parentID).Scan(&existing); {
	case e == nil:
		eff, err := scanEffect(tx.conn.QueryRowContext(ctx, `SELECT `+effectColumns+` FROM effects WHERE id=?`, existing))
		return eff, err
	case !errors.Is(e, sql.ErrNoRows):
		return workflow.Effect{}, e
	}
	var ordinal int
	if err := tx.conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),-1)+1 FROM effects WHERE source_event_id=?`, source).Scan(&ordinal); err != nil {
		return workflow.Effect{}, err
	}
	id, now := effectID(source, ordinal), s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO effects(id,run_id,attempt_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms)
		VALUES(?,?,?,?,?,?,?,?,'pending',0,?,?)`, id, run, nullable(attempt.String), source, kind, ordinal, payloadHash, effectNonce(source, ordinal), now, now); err != nil {
		return workflow.Effect{}, err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('effect',?,'payload',?)`, id, payloadHash); err != nil {
		return workflow.Effect{}, err
	}
	eff, err := scanEffect(tx.conn.QueryRowContext(ctx, `SELECT `+effectColumns+` FROM effects WHERE id=?`, id))
	if err != nil {
		return workflow.Effect{}, err
	}
	return eff, tx.commit(ctx)
}

// RunEffects lists a run's effects in creation order, for recovery and status.
func (s *Store) RunEffects(ctx context.Context, runID string) ([]workflow.Effect, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+effectColumns+` FROM effects WHERE run_id=? ORDER BY created_ms,source_event_id,ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workflow.Effect
	for rows.Next() {
		e, err := scanEffect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AttemptJobs lists an attempt's jobs of one kind, oldest first.
func (s *Store) AttemptJobs(ctx context.Context, attemptID, kind string) ([]workflow.Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM jobs WHERE attempt_id=? AND kind=? ORDER BY COALESCE(started_ms,0), id`, attemptID, kind)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []workflow.Job
	for _, id := range ids {
		j, ok, err := s.GetJob(ctx, id)
		if err != nil || !ok {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// CancelPendingEffects cancels a run's effects of the given kinds that have not
// been claimed, so work that was never started never starts, and ends the jobs
// those effects were to start: a job whose prompt was never sent is cancelled,
// not left open. It returns how many effects were cancelled. Effects already
// delivering or delivered, and their jobs, are untouched.
func (s *Store) CancelPendingEffects(ctx context.Context, runID string, kinds []string) (int, error) {
	if len(kinds) == 0 {
		return 0, fmt.Errorf("%w: CancelPendingEffects needs at least one kind", workflow.ErrInvalidTransition)
	}
	marks := strings.Repeat("?,", len(kinds))
	args := []any{runID}
	for _, k := range kinds {
		args = append(args, k)
	}
	in := `run_id=? AND state='pending' AND kind IN (` + marks[:len(marks)-1] + `)`
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.rollback()
	// An effect has no job of its own: a builder prompt is for its attempt's
	// builder job and a review prompt for its reviewer job.
	jobArgs := append([]any{s.now().UnixMilli()}, args...)
	if _, err := tx.conn.ExecContext(ctx, `UPDATE jobs SET state='cancelled', completed_ms=? WHERE state IN ('pending','running')
		AND EXISTS (SELECT 1 FROM effects WHERE attempt_id=jobs.attempt_id AND `+in+`
			AND kind=CASE jobs.kind WHEN 'builder' THEN 'builder_dispatch' WHEN 'reviewer' THEN 'review_prompt' END)`, jobArgs...); err != nil {
		return 0, err
	}
	res, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='cancelled' WHERE `+in, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.commit(ctx)
}

// RenewLease extends a lease the caller still holds: its owner and fence must
// match and it must still be owned (not reconciling or released). It never
