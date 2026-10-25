package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Applying a staged review candidate. A reviewer's result is staged by the
// mailbox and applied here, after the controller has seen the reviewer settle
// and the reviewed tree still in place. Time passes between that observation
// and the write: a person may stop the run, another pause may begin, a new
// writer may take the lease or the attempt may be replaced. So the write is
// guarded by a fence read before the observation. If anything in it has moved
// the write is a quiet no-op and the controller starts again from fresh
// reads. A miss never schedules anything, and a hit clears a pause only when
// it is the review_unknown pause waiting on this very job.

// ReviewFence is what a candidate's application must still find true when it
// commits. Every field is compared; none stands in for another.
type ReviewFence struct {
	RunVersion, PauseGeneration, StopGeneration, LeaseFence int64
	AttemptID, PauseReason, PauseJobID, TreeHash            string
}

func reviewFenceMatches(expected, current ReviewFence) bool { return expected == current }

// ReviewApplyRequest asks to apply a staged candidate under a fence. The
// fence fields are the values the controller read before it observed the
// reviewer; PatchHash and TreeHash are the evidence the job reviewed.
type ReviewApplyRequest struct {
	CandidateID, RunID, AttemptID, JobID, EffectID string
	PatchHash, TreeHash, PauseReason, PauseJobID   string
	RunVersion, PauseGeneration, StopGeneration    int64
	LeaseFence                                     int64
}

// ReviewApplyResult says what the guarded write did. A stale fence is
// Applied false with no error; ClearedPause is true only when the matching
// review_unknown pause was cleared by this call.
type ReviewApplyResult struct{ Applied, ClearedPause bool }

func (r ReviewApplyRequest) fence() ReviewFence {
	return ReviewFence{RunVersion: r.RunVersion, PauseGeneration: r.PauseGeneration, StopGeneration: r.StopGeneration,
		LeaseFence: r.LeaseFence, AttemptID: r.AttemptID, PauseReason: r.PauseReason, PauseJobID: r.PauseJobID, TreeHash: r.TreeHash}
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// currentFence reads the fence as it stands now. An attempt that is not the
// current one reads as no attempt, a lease that is not held reads as fence 0,
// and a run that is neither paused nor stopping reads as no pause.
func currentFence(ctx context.Context, q rowQuerier, runID, attemptID string) (ReviewFence, error) {
	var f ReviewFence
	var state, code, job sql.NullString
	var checkout string
	err := q.QueryRowContext(ctx, `SELECT r.version,r.pause_generation,r.stop_generation,r.state,r.pause_code,r.pause_job_id,p.checkout_id
		FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`, runID).
		Scan(&f.RunVersion, &f.PauseGeneration, &f.StopGeneration, &state, &code, &job, &checkout)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("no run %s", runID)
	}
	if err != nil {
		return f, err
	}
	if state.String == "paused" || state.String == "stopping" {
		f.PauseReason, f.PauseJobID = code.String, job.String
	}
	var tree sql.NullString
	switch err := q.QueryRowContext(ctx, `SELECT end_snapshot_hash FROM attempts WHERE id=? AND run_id=? AND is_current=1`, attemptID, runID).Scan(&tree); {
	case err == nil:
		f.AttemptID, f.TreeHash = attemptID, tree.String
	case !errors.Is(err, sql.ErrNoRows):
		return f, err
	}
	err = q.QueryRowContext(ctx, `SELECT fence FROM checkout_leases WHERE checkout_id=? AND state='owned'`, checkout).Scan(&f.LeaseFence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, err
	}
	return f, nil
}

// CurrentReviewFence reads the fence now, for a controller to hold while it
// observes the reviewer.
func (s *Store) CurrentReviewFence(ctx context.Context, runID, attemptID string) (ReviewFence, error) {
	return currentFence(ctx, s.db, runID, attemptID)
}

// ApplyReviewCandidate marks a staged candidate applied, acknowledges its
// prompt effect and clears the run's review_unknown pause when that pause
// waits on this candidate's job, all in one transaction that first rechecks
// the whole fence. Applying an applied candidate again changes nothing and
// reports Applied. The caller has already validated the settled reviewer and
// the tree on disk; a transaction cannot freeze the working tree.
func (s *Store) ApplyReviewCandidate(ctx context.Context, req ReviewApplyRequest) (ReviewApplyResult, error) {
	if err := s.writable(); err != nil {
		return ReviewApplyResult{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return ReviewApplyResult{}, err
	}
	defer tx.rollback()
	cand, err := scanCandidate(tx.conn.QueryRowContext(ctx, `SELECT `+candidateColumns+` FROM review_candidates WHERE id=?`, req.CandidateID))
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewApplyResult{}, nil
	} else if err != nil {
		return ReviewApplyResult{}, err
	}
	if cand.RunID != req.RunID || cand.AttemptID != req.AttemptID || cand.JobID != req.JobID || cand.EffectID != req.EffectID {
		return ReviewApplyResult{}, nil
	}
	switch cand.State {
	case "applied":
		return ReviewApplyResult{Applied: true}, nil
	case "staged":
	default:
		return ReviewApplyResult{}, nil
	}
	now, err := currentFence(ctx, tx.conn, req.RunID, req.AttemptID)
	if err != nil {
		return ReviewApplyResult{}, err
	}
	if !reviewFenceMatches(req.fence(), now) {
		return ReviewApplyResult{}, nil
	}
	var patch sql.NullString
	if err := tx.conn.QueryRowContext(ctx, `SELECT patch_hash FROM attempts WHERE id=?`, req.AttemptID).Scan(&patch); err != nil {
		return ReviewApplyResult{}, err
	}
	if patch.String != req.PatchHash {
		return ReviewApplyResult{}, nil
	}
	ms := s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `UPDATE review_candidates SET state='applied', applied_ms=? WHERE id=? AND state='staged'`, ms, cand.ID); err != nil {
		return ReviewApplyResult{}, err
	}
	cleared, err := resolveReviewPrompt(ctx, tx, s.now().UnixMilli(), req.RunID, req.JobID, req.EffectID, "acknowledged")
	if err != nil {
		return ReviewApplyResult{}, err
	}
	return ReviewApplyResult{Applied: true, ClearedPause: cleared}, tx.commit(ctx)
}

// ResolveReviewPrompt settles review prompt effectID of reviewer job jobID in
// run runID, and clears the run's review_unknown pause when that pause waits on
// this job, in one transaction. to is "acknowledged" when a recorded result
// answered the prompt, "cancelled" when a review waiver made it moot. It
// reports whether the pause was cleared.
func (s *Store) ResolveReviewPrompt(ctx context.Context, runID, jobID, effectID, to string) (bool, error) {
	if to != "acknowledged" && to != "cancelled" {
		return false, invalid("a review prompt is resolved as acknowledged or cancelled")
	}
	if err := s.writable(); err != nil {
		return false, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.rollback()
	cleared, err := resolveReviewPrompt(ctx, tx, s.now().UnixMilli(), runID, jobID, effectID, to)
	if err != nil {
		return false, err
	}
	return cleared, tx.commit(ctx)
}

// resolveReviewPrompt moves the prompt, if still delivering or
// delivery_unknown, to state to; an acknowledged, cancelled or failed effect is
// left. It then clears the run's review_unknown pause only if the pause waits on
// jobID, or names no job and waits on this prompt. A pane review's prompt is
// bound to no job, so its pause names only the prompt.
func resolveReviewPrompt(ctx context.Context, tx *dbTx, nowMS int64, runID, jobID, effectID, to string) (bool, error) {
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state=?, lease_owner=NULL, lease_until_ms=NULL
		WHERE id=? AND state IN ('delivering','delivery_unknown')`, to, effectID); err != nil {
		return false, err
	}
	if to == "cancelled" {
		// A prompt no worker ever claimed and that has no receipt was certainly
		// never sent, so its reservation goes back in this same transaction. One
		// that was sent keeps its reservation: its session's usage settles it.
		res, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='cancelled' WHERE id=? AND state='pending' AND receipt_hash IS NULL`, effectID)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			if err := releaseEffectAdmissions(ctx, tx, nowMS, effectID); err != nil {
				return false, err
			}
		}
	}
	out, err := tx.conn.ExecContext(ctx, `UPDATE runs SET state='running', pause_code=NULL, pause_job_id=NULL, pause_effect_id=NULL
		WHERE id=? AND state='paused' AND pause_code='review_unknown'
		AND (pause_job_id=? OR (pause_job_id IS NULL AND pause_effect_id=?))`, runID, jobID, effectID)
	if err != nil {
		return false, err
	}
	n, _ := out.RowsAffected()
	return n == 1, nil
}

// CancelReviewJobs ends the attempt's open review jobs, which a waived review
// makes moot, so the reviewer's usage source can be final and its collaboration
// reservation settles from what the reviewer spent. The planner's independent
// verification is another stage and is left alone.
func (s *Store) CancelReviewJobs(ctx context.Context, attemptID string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='cancelled', completed_ms=? WHERE attempt_id=? AND kind='reviewer'
		AND state IN ('pending','running')
		AND NOT EXISTS (SELECT 1 FROM review_stages s WHERE s.job_id=jobs.id AND s.stage='independent_verification')`,
		s.now().UnixMilli(), attemptID)
	return err
}

// RejectReviewCandidate marks a staged candidate refused, with the reason, so
// it is not tried again. The bytes stay as evidence.
func (s *Store) RejectReviewCandidate(ctx context.Context, candidateID, reason string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if reason == "" {
		return invalid("a refused candidate says why")
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE review_candidates SET state='rejected', reject_reason=? WHERE id=? AND state='staged'`, reason, candidateID)
	return err
}

// StagedReviewCandidates lists a run's candidates still waiting to be applied,
// oldest first.
func (s *Store) StagedReviewCandidates(ctx context.Context, runID string) ([]ReviewCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+candidateColumns+` FROM review_candidates WHERE run_id=? AND state='staged' ORDER BY created_ms, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewCandidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StalledReview is a started review that has run past its execution deadline
// with no candidate.
type StalledReview struct{ JobID, EffectID string }

// StalledReviews lists a run's reviews that started and passed their
// execution deadline before submitting. A delivery that is merely unknown is
// not stalled: only a verified start has a deadline.
func (s *Store) StalledReviews(ctx context.Context, runID string, nowMS int64) ([]StalledReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT st.job_id, st.effect_id FROM review_starts st
		JOIN jobs j ON j.id=st.job_id JOIN attempts a ON a.id=j.attempt_id
		WHERE a.run_id=? AND a.is_current=1 AND j.state='running' AND st.deadline_ms<?
		AND NOT EXISTS (SELECT 1 FROM review_candidates c WHERE c.job_id=st.job_id) ORDER BY st.deadline_ms, st.job_id`, runID, nowMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StalledReview
	for rows.Next() {
		var r StalledReview
		if err := rows.Scan(&r.JobID, &r.EffectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordedReviewResult returns the hash of the result already recorded for a
// reviewer job, if any. A staged candidate whose bytes are that result is the
// one that produced it, so applying it again is the end of a half-finished
// application, not a second review.
func (s *Store) RecordedReviewResult(ctx context.Context, jobID string) (string, bool, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT result_hash FROM review_results WHERE job_id=?`, jobID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}
