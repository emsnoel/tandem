package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// RecordAttemptEvidence stores the end snapshot and patch the controller
// captured for an attempt, before its submission is applied. A hash is the
// controller's attestation that the capture was complete, so incomplete
// captures are never recorded. Repeating the same values is harmless; changing
// them once the attempt has submitted is a conflict.
func (s *Store) RecordAttemptEvidence(ctx context.Context, attemptID, endSnapshotHash, patchHash string) error {
	if !hashPattern.MatchString(endSnapshotHash) || !hashPattern.MatchString(patchHash) {
		return fmt.Errorf("%w: end snapshot and patch must be 64 lowercase hex hashes", workflow.ErrInvalidTransition)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var impl string
	var current int
	var end, patch sql.NullString
	switch e := tx.conn.QueryRowContext(ctx, `SELECT implementation_state,is_current,end_snapshot_hash,patch_hash FROM attempts WHERE id=?`, attemptID).Scan(&impl, &current, &end, &patch); {
	case errors.Is(e, sql.ErrNoRows):
		return fmt.Errorf("%w: attempt %s does not exist", workflow.ErrConflict, attemptID)
	case e != nil:
		return e
	}
	if current != 1 {
		return fmt.Errorf("%w: attempt %s was superseded", workflow.ErrStaleAttempt, attemptID)
	}
	same := end.String == endSnapshotHash && patch.String == patchHash
	if impl == "submitted" && !same {
		return fmt.Errorf("%w: attempt %s already submitted different evidence", workflow.ErrConflict, attemptID)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE attempts SET end_snapshot_hash=?, patch_hash=? WHERE id=?`, endSnapshotHash, patchHash, attemptID); err != nil {
		return err
	}
	for _, r := range [][2]string{{"end_snapshot", endSnapshotHash}, {"patch", patchHash}} {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('attempt',?,?,?)`, attemptID, r[0], r[1]); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

// ReviewRecord is a validated review result to persist.
type ReviewRecord struct {
	JobID, AttemptID                string
	ResultHash, PatchHash, TreeHash string
	Status                          string
	// IdentityHash optionally names the record of who reviewed and how much is
	// known about them.
	IdentityHash string
	// DistinctFromWriter says the reviewer was not the writer. Only a distinct
	// independent verification can satisfy acceptance.
	DistinctFromWriter bool
}

// RecordReviewResult stores a review result and completes its reviewer job in
// one transaction. A job is completed once.
func (s *Store) RecordReviewResult(ctx context.Context, r ReviewRecord) error {
	if !idPattern.MatchString(r.JobID) || !idPattern.MatchString(r.AttemptID) {
		return fmt.Errorf("%w: job and attempt ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	}
	for _, h := range []string{r.ResultHash, r.PatchHash, r.TreeHash} {
		if !hashPattern.MatchString(h) {
			return fmt.Errorf("%w: malformed review hash %q", workflow.ErrInvalidTransition, h)
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var kind, state, attempt string
	switch e := tx.conn.QueryRowContext(ctx, `SELECT kind,state,attempt_id FROM jobs WHERE id=?`, r.JobID).Scan(&kind, &state, &attempt); {
	case errors.Is(e, sql.ErrNoRows):
		return fmt.Errorf("%w: job %s does not exist", workflow.ErrConflict, r.JobID)
	case e != nil:
		return e
	}
	if kind != "reviewer" || attempt != r.AttemptID {
		return fmt.Errorf("%w: job %s is not a reviewer job of attempt %s", workflow.ErrConflict, r.JobID, r.AttemptID)
	}
	if state != "running" && state != "pending" {
		return fmt.Errorf("%w: job %s is already %s", workflow.ErrConflict, r.JobID, state)
	}
	now := s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO review_results(job_id,attempt_id,result_hash,patch_hash,tree_hash,status,distinct_from_writer,created_ms) VALUES(?,?,?,?,?,?,?,?)`,
		r.JobID, r.AttemptID, r.ResultHash, r.PatchHash, r.TreeHash, r.Status, r.DistinctFromWriter, now); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: job %s already has a result", workflow.ErrConflict, r.JobID)
		}
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE jobs SET state='completed', result_hash=?, completed_ms=? WHERE id=?`, r.ResultHash, now, r.JobID); err != nil {
		return err
	}
	refs := [][2]string{{"result", r.ResultHash}, {"patch", r.PatchHash}, {"tree", r.TreeHash}}
	if r.IdentityHash != "" {
		if !hashPattern.MatchString(r.IdentityHash) {
			return fmt.Errorf("%w: malformed reviewer identity hash %q", workflow.ErrInvalidTransition, r.IdentityHash)
		}
		refs = append(refs, [2]string{"identity", r.IdentityHash})
	}
	for _, x := range refs {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('review_result',?,?,?)`, r.JobID, x[0], x[1]); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

// Waiver is the planner's recorded decision to overrule one requirement.
type Waiver struct {
	ID, AttemptID, Scope, Actor  string
	ReasonHash, EvidenceTreeHash string
}

// RecordWaiver stores a waiver. It must carry a reason, an actor and the tree
// the planner examined, which has to be the attempt's current end snapshot: a
// waiver for a tree that has since changed overrules nothing.
func (s *Store) RecordWaiver(ctx context.Context, w Waiver) error {
	switch {
	case !idPattern.MatchString(w.ID) || !idPattern.MatchString(w.AttemptID):
		return fmt.Errorf("%w: waiver and attempt ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	case w.Scope != "implementation" && w.Scope != "check" && w.Scope != "review":
		return fmt.Errorf("%w: a waiver names implementation, check or review, not %q", workflow.ErrInvalidTransition, w.Scope)
	case w.Actor == "":
		return fmt.Errorf("%w: a waiver needs an actor", workflow.ErrInvalidTransition)
	case !hashPattern.MatchString(w.ReasonHash) || !hashPattern.MatchString(w.EvidenceTreeHash):
		return fmt.Errorf("%w: a waiver needs a reason artifact and the tree it examined", workflow.ErrInvalidTransition)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var end sql.NullString
	switch e := tx.conn.QueryRowContext(ctx, `SELECT end_snapshot_hash FROM attempts WHERE id=? AND is_current=1`, w.AttemptID).Scan(&end); {
	case errors.Is(e, sql.ErrNoRows):
		return fmt.Errorf("%w: attempt %s is not a current attempt", workflow.ErrStaleAttempt, w.AttemptID)
	case e != nil:
		return e
	}
	if end.String != w.EvidenceTreeHash {
		return fmt.Errorf("%w: the waiver examined tree %s but the attempt is at %s", workflow.ErrConflict, w.EvidenceTreeHash, end.String)
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO waivers(id,attempt_id,scope,reason_hash,actor,evidence_tree_hash,created_ms) VALUES(?,?,?,?,?,?,?)`,
		w.ID, w.AttemptID, w.Scope, w.ReasonHash, w.Actor, w.EvidenceTreeHash, s.now().UnixMilli()); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: waiver %s already exists", workflow.ErrConflict, w.ID)
		}
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('waiver',?,'reason',?)`, w.ID, w.ReasonHash); err != nil {
		return err
	}
	return tx.commit(ctx)
}
