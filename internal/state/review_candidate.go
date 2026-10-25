package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Review stages and candidates. A reviewer's result is staged here as the exact
// bytes it submitted, bound to its registered reviewer job. Staging never
// accepts anything: the controller applies a candidate separately, after the
// reviewer has settled.

// Review stages. Every acceptance needs both, in this order of authority: the
// reviewer pane's review, then the planner's own independent verification.
const (
	StageBuilderReview           = "builder_review"
	StageIndependentVerification = "independent_verification"
)

// ReviewStart is a verified review start.
type ReviewStart struct {
	JobID, EffectID, EventID, SessionID, ObservedSession string
	StartedMS, DeadlineMS                                int64
}

// ReviewStartRequest records a start. Deadline is how long after now the
// review may run before it counts as stalled.
type ReviewStartRequest struct {
	JobID, EffectID, EventID, SessionID, ObservedSession string
	Deadline                                             time.Duration
}

// ReviewCandidate is one staged result.
type ReviewCandidate struct {
	ID, RunID, AttemptID, JobID, EffectID, EventID, Stage, PayloadHash, State string
}

// ReviewCandidateRequest stages a result. Its stage is not a field: it comes
// from the job's registration.
type ReviewCandidateRequest struct {
	RunID, AttemptID, JobID, EffectID, EventID, PayloadHash string
}

// RegisterReviewStage records which mandatory stage a reviewer job serves.
// Registering the same stage again is a no-op; another stage is ErrConflict.
func (s *Store) RegisterReviewStage(ctx context.Context, jobID, stage string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if stage != StageBuilderReview && stage != StageIndependentVerification {
		return invalid("%q is not a review stage", stage)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var kind string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT kind FROM jobs WHERE id=?`, jobID).Scan(&kind); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no job %s", workflow.ErrConflict, jobID)
	case err != nil:
		return err
	case kind != "reviewer":
		return invalid("job %s is a %s job, not a reviewer", jobID, kind)
	}
	var have string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT stage FROM review_stages WHERE job_id=?`, jobID).Scan(&have); {
	case err == nil && have == stage:
		return nil
	case err == nil:
		return fmt.Errorf("%w: job %s already serves the %s stage", workflow.ErrConflict, jobID, have)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO review_stages(job_id,stage,created_ms) VALUES(?,?,?)`, jobID, stage, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// ReviewStage returns the stage a reviewer job was registered for.
func (s *Store) ReviewStage(ctx context.Context, jobID string) (string, bool, error) {
	var stage string
	err := s.db.QueryRowContext(ctx, `SELECT stage FROM review_stages WHERE job_id=?`, jobID).Scan(&stage)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return stage, err == nil, err
}

// BindEffectJob records that a review prompt effect asks for a given reviewer
// job, so a message naming both can be checked against what was sent. Binding
// again to the same job is a no-op; to another is ErrConflict.
func (s *Store) BindEffectJob(ctx context.Context, effectID, jobID string) error {
	if err := s.writable(); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var attempt, bound sql.NullString
	switch err := tx.conn.QueryRowContext(ctx, `SELECT attempt_id,job_id FROM effects WHERE id=?`, effectID).Scan(&attempt, &bound); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no effect %s", workflow.ErrConflict, effectID)
	case err != nil:
		return err
	case bound.Valid && bound.String == jobID:
		return nil
	case bound.Valid:
		return fmt.Errorf("%w: effect %s is bound to job %s", workflow.ErrConflict, effectID, bound.String)
	}
	var jobAttempt string
	if err := tx.conn.QueryRowContext(ctx, `SELECT attempt_id FROM jobs WHERE id=?`, jobID).Scan(&jobAttempt); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no job %s", workflow.ErrConflict, jobID)
	} else if err != nil {
		return err
	} else if jobAttempt != attempt.String {
		return invalid("job %s belongs to another attempt than effect %s", jobID, effectID)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET job_id=? WHERE id=?`, jobID, effectID); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// RecordReviewStart stores a verified start. A repeat of the same start, or a
// start from the same session under another event, is a duplicate; a start
// from another session for the same job is ErrConflict.
func (s *Store) RecordReviewStart(ctx context.Context, req ReviewStartRequest) (ReviewStart, bool, error) {
	if err := s.writable(); err != nil {
		return ReviewStart{}, false, err
	}
	if req.Deadline <= 0 {
		return ReviewStart{}, false, invalid("a review start carries an execution deadline")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return ReviewStart{}, false, err
	}
	defer tx.rollback()
	got, found, err := reviewStartTx(ctx, tx.conn, req.JobID)
	if err != nil {
		return ReviewStart{}, false, err
	}
	if found {
		if got.SessionID != req.SessionID && got.SessionID != "" && req.SessionID != "" {
			return got, false, fmt.Errorf("%w: job %s started in session %s, not %s", workflow.ErrConflict, req.JobID, got.SessionID, req.SessionID)
		}
		return got, true, nil
	}
	now := s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO review_starts(job_id,effect_id,event_id,session_id,observed_session,started_ms,deadline_ms)
		VALUES(?,?,?,?,?,?,?)`, req.JobID, req.EffectID, req.EventID, nullable(req.SessionID), nullable(req.ObservedSession), now,
		now+req.Deadline.Milliseconds()); err != nil {
		if isForeignKey(err) {
			return ReviewStart{}, false, invalid("start names a job or effect that does not exist")
		}
		return ReviewStart{}, false, err
	}
	if err := tx.commit(ctx); err != nil {
		return ReviewStart{}, false, err
	}
	return ReviewStart{JobID: req.JobID, EffectID: req.EffectID, EventID: req.EventID, SessionID: req.SessionID,
		ObservedSession: req.ObservedSession, StartedMS: now, DeadlineMS: now + req.Deadline.Milliseconds()}, false, nil
}

func reviewStartTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, jobID string) (ReviewStart, bool, error) {
	var r ReviewStart
	err := q.QueryRowContext(ctx, `SELECT job_id,effect_id,event_id,COALESCE(session_id,''),COALESCE(observed_session,''),started_ms,deadline_ms
		FROM review_starts WHERE job_id=?`, jobID).Scan(&r.JobID, &r.EffectID, &r.EventID, &r.SessionID, &r.ObservedSession, &r.StartedMS, &r.DeadlineMS)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// ReviewStartFor returns a reviewer job's verified start, if it has one.
func (s *Store) ReviewStartFor(ctx context.Context, jobID string) (ReviewStart, bool, error) {
	return reviewStartTx(ctx, s.db, jobID)
}

const candidateColumns = `id,run_id,attempt_id,job_id,effect_id,event_id,stage,payload_hash,state`

func scanCandidate(r rowScanner) (ReviewCandidate, error) {
	var c ReviewCandidate
	err := r.Scan(&c.ID, &c.RunID, &c.AttemptID, &c.JobID, &c.EffectID, &c.EventID, &c.Stage, &c.PayloadHash, &c.State)
	return c, err
}

// StageReviewCandidate stores the exact result bytes (already an artifact) of
// a reviewer job. The job must be a registered reviewer job of the attempt and
// the effect must be bound to it. The same bytes again, under whatever event
// id, return the stored candidate as a duplicate; other bytes for the job are
// ErrConflict. Nothing here accepts or schedules anything.
func (s *Store) StageReviewCandidate(ctx context.Context, req ReviewCandidateRequest) (ReviewCandidate, bool, error) {
	if err := s.writable(); err != nil {
		return ReviewCandidate{}, false, err
	}
	if !hashPattern.MatchString(req.PayloadHash) {
		return ReviewCandidate{}, false, invalid("a candidate is an artifact hash")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return ReviewCandidate{}, false, err
	}
	defer tx.rollback()
	if prior, err := scanCandidate(tx.conn.QueryRowContext(ctx, `SELECT `+candidateColumns+` FROM review_candidates WHERE job_id=? OR event_id=?`,
		req.JobID, req.EventID)); err == nil {
		if prior.PayloadHash != req.PayloadHash || prior.JobID != req.JobID {
			return prior, false, fmt.Errorf("%w: job %s already has a candidate with other bytes", workflow.ErrConflict, prior.JobID)
		}
		return prior, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ReviewCandidate{}, false, err
	}
	var jobAttempt, kind string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT attempt_id,kind FROM jobs WHERE id=?`, req.JobID).Scan(&jobAttempt, &kind); {
	case errors.Is(err, sql.ErrNoRows):
		return ReviewCandidate{}, false, invalid("no reviewer job %s", req.JobID)
	case err != nil:
		return ReviewCandidate{}, false, err
	case kind != "reviewer" || jobAttempt != req.AttemptID:
		return ReviewCandidate{}, false, invalid("job %s is not a reviewer job of attempt %s", req.JobID, req.AttemptID)
	}
	var stage string
	if err := tx.conn.QueryRowContext(ctx, `SELECT stage FROM review_stages WHERE job_id=?`, req.JobID).Scan(&stage); errors.Is(err, sql.ErrNoRows) {
		return ReviewCandidate{}, false, invalid("reviewer job %s has no registered review stage", req.JobID)
	} else if err != nil {
		return ReviewCandidate{}, false, err
	}
	var bound sql.NullString
	if err := tx.conn.QueryRowContext(ctx, `SELECT job_id FROM effects WHERE id=? AND run_id=? AND attempt_id=?`,
		req.EffectID, req.RunID, req.AttemptID).Scan(&bound); errors.Is(err, sql.ErrNoRows) {
		return ReviewCandidate{}, false, invalid("effect %s is not an effect of attempt %s", req.EffectID, req.AttemptID)
	} else if err != nil {
		return ReviewCandidate{}, false, err
	} else if !bound.Valid || bound.String != req.JobID {
		return ReviewCandidate{}, false, invalid("effect %s is not bound to reviewer job %s", req.EffectID, req.JobID)
	}
	c := ReviewCandidate{ID: req.EventID, RunID: req.RunID, AttemptID: req.AttemptID, JobID: req.JobID, EffectID: req.EffectID,
		EventID: req.EventID, Stage: stage, PayloadHash: req.PayloadHash, State: "staged"}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO review_candidates(id,run_id,attempt_id,job_id,effect_id,event_id,stage,payload_hash,state,created_ms)
		VALUES(?,?,?,?,?,?,?,?,'staged',?)`, c.ID, c.RunID, c.AttemptID, c.JobID, c.EffectID, c.EventID, c.Stage, c.PayloadHash,
		s.now().UnixMilli()); err != nil {
		if isForeignKey(err) {
			return ReviewCandidate{}, false, invalid("candidate names a run, attempt or artifact that does not exist")
		}
		return ReviewCandidate{}, false, err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('review_candidate',?,'result',?)`,
		c.ID, c.PayloadHash); err != nil {
		return ReviewCandidate{}, false, err
	}
	return c, false, tx.commit(ctx)
}

// ReviewCandidateFor returns a reviewer job's staged candidate, if it has one.
func (s *Store) ReviewCandidateFor(ctx context.Context, jobID string) (ReviewCandidate, bool, error) {
	c, err := scanCandidate(s.db.QueryRowContext(ctx, `SELECT `+candidateColumns+` FROM review_candidates WHERE job_id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}
