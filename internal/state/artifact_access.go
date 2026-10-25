package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// AttemptArtifacts returns every artifact an attempt may be shown, with the
// reason: what the attempt owns (its packet, snapshots, evidence), what its
// jobs and their reviews produced, and its own task's brief, constraints and
// spec. It is the state half of retrieval authorization; the controller adds
// the memory a packet's manifest named.
//
// Nothing else is included, not another attempt's or task's artifacts and not
// the plan, so a hash is no more than a name: knowing one grants nothing. The
// attempt must exist and be the current attempt of its task.
func (s *Store) AttemptArtifacts(ctx context.Context, attemptID string) (map[string]string, error) {
	var runID, taskID string
	var current int
	switch err := s.db.QueryRowContext(ctx, `SELECT run_id,task_id,is_current FROM attempts WHERE id=?`, attemptID).Scan(&runID, &taskID, &current); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w: attempt %s does not exist", workflow.ErrConflict, attemptID)
	case err != nil:
		return nil, err
	}
	if current != 1 {
		return nil, fmt.Errorf("%w: attempt %s was superseded", workflow.ErrStaleAttempt, attemptID)
	}
	out := map[string]string{}
	add := func(q string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var role, hash string
			if err := rows.Scan(&role, &hash); err != nil {
				return err
			}
			if _, seen := out[hash]; !seen {
				out[hash] = role
			}
		}
		return rows.Err()
	}
	if err := add(`SELECT 'attempt '||role, hash FROM artifact_refs WHERE owner_kind='attempt' AND owner_id=? ORDER BY role`, attemptID); err != nil {
		return nil, err
	}
	if err := add(`SELECT 'job '||j.kind||' '||r.role, r.hash FROM artifact_refs r JOIN jobs j ON j.id=r.owner_id
		WHERE r.owner_kind='job' AND j.attempt_id=? ORDER BY j.id, r.role`, attemptID); err != nil {
		return nil, err
	}
	if err := add(`SELECT 'review '||r.role, r.hash FROM artifact_refs r JOIN jobs j ON j.id=r.owner_id
		WHERE r.owner_kind='review_result' AND j.attempt_id=? ORDER BY j.id, r.role`, attemptID); err != nil {
		return nil, err
	}
	if err := add(`SELECT 'task brief', brief_hash FROM tasks WHERE run_id=? AND id=?
		UNION ALL SELECT 'task constraints', constraints_hash FROM tasks WHERE run_id=? AND id=?
		UNION ALL SELECT 'task spec', spec_hash FROM tasks WHERE run_id=? AND id=?`, runID, taskID, runID, taskID, runID, taskID); err != nil {
		return nil, err
	}
	return out, nil
}

// AttemptRun returns the run an attempt belongs to.
func (s *Store) AttemptRun(ctx context.Context, attemptID string) (string, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM attempts WHERE id=?`, attemptID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: attempt %s does not exist", workflow.ErrConflict, attemptID)
	}
	return runID, err
}

// LatestReviewResult returns the hash of the most recent review result recorded
// for an attempt, or false when it has none.
func (s *Store) LatestReviewResult(ctx context.Context, attemptID string) (string, bool, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT result_hash FROM review_results WHERE attempt_id=? ORDER BY created_ms DESC, rowid DESC LIMIT 1`, attemptID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}
