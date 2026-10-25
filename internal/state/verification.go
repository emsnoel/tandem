package state

import (
	"context"
	"database/sql"
	"errors"

	"github.com/emsnoel/tandem/internal/workflow"
)

// VerificationOutcome is what an attempt's independent verification has
// recorded. Found says a result exists. Distinct says its reviewer was not the
// writer. Valid says it also counts: the job completed and the result is
// bound to the attempt's current patch and tree.
type VerificationOutcome struct {
	Found, Distinct, Valid bool
	Status                 workflow.ReviewState
	JobID                  string
}

// The two kinds of independent check: the planner's verification stage, and
// the review of a reviewer pane, which is every review result that is not that
// stage.
const (
	plannerVerification = `JOIN review_stages s ON s.job_id=r.job_id AND s.stage='independent_verification'
		WHERE r.attempt_id=? AND j.kind='reviewer'`
	reviewerPaneReview = `WHERE r.attempt_id=? AND j.kind='reviewer'
		AND NOT EXISTS (SELECT 1 FROM review_stages s WHERE s.job_id=r.job_id AND s.stage='independent_verification')`
)

func verificationOutcome(ctx context.Context, q rowQuerier, a workflow.Attempt) (VerificationOutcome, error) {
	return independentOutcome(ctx, q, a, plannerVerification)
}

func independentOutcome(ctx context.Context, q rowQuerier, a workflow.Attempt, which string) (VerificationOutcome, error) {
	var v VerificationOutcome
	var status, patch, tree, jobState string
	var distinct bool
	err := q.QueryRowContext(ctx, `SELECT r.job_id,r.status,r.patch_hash,r.tree_hash,r.distinct_from_writer,j.state
		FROM review_results r JOIN jobs j ON j.id=r.job_id `+which+` ORDER BY r.created_ms DESC, r.job_id DESC LIMIT 1`, a.ID).
		Scan(&v.JobID, &status, &patch, &tree, &distinct, &jobState)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return v, nil
	case err != nil:
		return v, err
	}
	v.Found, v.Distinct, v.Status = true, distinct, workflow.ReviewState(status)
	// Evidence counts only for the patch and tree it examined, from a reviewer
	// who is not the writer.
	v.Valid = jobState == "completed" && patch == a.PatchHash && tree == a.EndSnapshotHash && a.PatchHash != "" && distinct
	return v, nil
}

// reviewerOrVerification is the independent check of a run with a reviewer
// pane: the pane's review when it has one, and otherwise (a waived review, say)
// the planner's verification.
func reviewerOrVerification(ctx context.Context, q rowQuerier, a workflow.Attempt) (VerificationOutcome, error) {
	v, err := independentOutcome(ctx, q, a, reviewerPaneReview)
	if err != nil || v.Found {
		return v, err
	}
	return verificationOutcome(ctx, q, a)
}

// AttemptVerification reads an attempt's independent verification outcome.
func (s *Store) AttemptVerification(ctx context.Context, attemptID string) (VerificationOutcome, error) {
	a, err := s.GetAttempt(ctx, attemptID)
	if err != nil {
		return VerificationOutcome{}, err
	}
	return verificationOutcome(ctx, s.db, a)
}

// ReviewerOutcome reads the attempt's review as a reviewer pane's independent
// check: the latest review result that is not the planner's verification.
func (s *Store) ReviewerOutcome(ctx context.Context, attemptID string) (VerificationOutcome, error) {
	a, err := s.GetAttempt(ctx, attemptID)
	if err != nil {
		return VerificationOutcome{}, err
	}
	return independentOutcome(ctx, s.db, a, reviewerPaneReview)
}
