package state

import "context"

// ReviewRejection is the latest rejected handback for a review prompt.
// Notified tracks feedback for these exact payload bytes.
type ReviewRejection struct {
	RunID, AttemptID, JobID, EffectID, PayloadHash, Reason string
	Notified                                               bool
}

// RecordReviewRejection retains one rejection per effect. Repeating the same
// payload preserves the original reason and notification state.
func (s *Store) RecordReviewRejection(ctx context.Context, r ReviewRejection) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO review_rejections(effect_id,run_id,attempt_id,job_id,payload_hash,reason,notified,updated_seq)
 VALUES(?,?,?,?,?,?,0,(SELECT COALESCE(MAX(updated_seq),0)+1 FROM review_rejections)) ON CONFLICT(effect_id) DO UPDATE SET
 payload_hash=excluded.payload_hash,reason=excluded.reason,notified=0,updated_seq=excluded.updated_seq
 WHERE review_rejections.payload_hash<>excluded.payload_hash`, r.EffectID, r.RunID, r.AttemptID, r.JobID, r.PayloadHash, r.Reason)
	return err
}

// RunReviewRejections returns a run's feedback, oldest first.
func (s *Store) RunReviewRejections(ctx context.Context, runID string) ([]ReviewRejection, error) {
	version, err := s.SchemaVersion()
	if err != nil {
		return nil, err
	}
	// A status read opens old stores without migrating them. Rejection
	// feedback was added in schema 6 and cannot exist in an earlier store.
	if version < 6 {
		return []ReviewRejection{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,attempt_id,job_id,effect_id,payload_hash,reason,notified
 FROM review_rejections WHERE run_id=? ORDER BY updated_seq,effect_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ReviewRejection{}
	for rows.Next() {
		var r ReviewRejection
		if err := rows.Scan(&r.RunID, &r.AttemptID, &r.JobID, &r.EffectID, &r.PayloadHash, &r.Reason, &r.Notified); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// MarkReviewRejectionNotified acknowledges only the payload that was sent.
// A correction arriving meanwhile must still receive its own feedback.
func (s *Store) MarkReviewRejectionNotified(ctx context.Context, effectID, payloadHash string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE review_rejections SET notified=1 WHERE effect_id=? AND payload_hash=?`, effectID, payloadHash)
	return err
}

// ClearReviewRejection removes feedback after this job supplies a valid review.
func (s *Store) ClearReviewRejection(ctx context.Context, jobID string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM review_rejections WHERE job_id=?`, jobID)
	return err
}

// RetryReviewRejectionNotification releases a claim only after the transport
// proved it sent nothing. An uncertain send keeps its durable claim.
func (s *Store) RetryReviewRejectionNotification(ctx context.Context, effectID, payloadHash string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE review_rejections SET notified=0 WHERE effect_id=? AND payload_hash=?`, effectID, payloadHash)
	return err
}
