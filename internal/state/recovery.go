package state

// Bounded recovery: admitting the one automatic successor of a failed attempt.
//
// The allowance belongs to an origin budget group, not to a run or a task
// name. A task bound to logical work draws on every group that work carries, so
// a replacement run, a rename, a split or a merge reaches the same allowance.
// AdmitRepair consumes the allowance of all those groups or none, supersedes
// the failed attempt and creates its successor in one IMMEDIATE transaction.
// The admission row makes a retry return the same successor without consuming
// anything again, so a crash neither spends the allowance twice nor grants a
// fresh one.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/emsnoel/tandem/internal/workflow"
)

// ErrRepairExhausted: a budget group the work draws on has used its automatic
// repair. Nothing was consumed.
var ErrRepairExhausted = errors.New("automatic repair allowance exhausted")

// Causes of an automatic repair.
const (
	CauseReviewChanges = "review_changes"
	CauseCheckFailure  = "check_failure"
	CauseStuck         = "stuck"
)

// RecoveryRequest asks for the successor of one failed attempt. ID is the
// idempotency key: one per parent attempt. Attempt is the successor, already
// built; its start snapshot is the whole task's original tree, so the final
// diff keeps the failed attempt's retained edits and any manual changes.
type RecoveryRequest struct {
	ID, CollaborationID, RunID, TaskID, ParentAttemptID string
	ParentVersion                                       int64
	Cause                                               string
	EvidenceHash                                        string
	OriginalTreeHash, CurrentTreeHash                   string
	RunVersion, StopGeneration, LeaseFence              int64
	Attempt                                             AttemptSpec
}

// RecoveryAdmission is what an admission returns, new or repeated.
type RecoveryAdmission struct {
	ID, SuccessorID, LogicalWorkID string
	Groups                         []string
	Duplicate                      bool
}

func (r RecoveryRequest) validate() error {
	for name, id := range map[string]string{"request": r.ID, "collaboration": r.CollaborationID, "run": r.RunID, "parent attempt": r.ParentAttemptID} {
		if !idPattern.MatchString(id) {
			return invalid("the %s id must be 32 lowercase hex characters", name)
		}
	}
	if r.TaskID == "" {
		return invalid("a repair names its task")
	}
	switch r.Cause {
	case CauseReviewChanges, CauseCheckFailure, CauseStuck:
	default:
		return invalid("unknown repair cause %q", r.Cause)
	}
	for name, h := range map[string]string{"evidence": r.EvidenceHash, "original tree": r.OriginalTreeHash, "current tree": r.CurrentTreeHash} {
		if !hashPattern.MatchString(h) {
			return invalid("the %s must be a 64 lowercase hex hash", name)
		}
	}
	if err := validateAttemptSpec(r.Attempt); err != nil {
		return err
	}
	switch {
	case r.Attempt.RunID != r.RunID || r.Attempt.TaskID != r.TaskID || r.Attempt.ParentID != r.ParentAttemptID:
		return invalid("the successor must belong to the same run and task and name the failed attempt as its parent")
	case r.Attempt.StartSnapshotHash != r.OriginalTreeHash:
		return invalid("the successor starts from the task's original tree, not from the tree the failure left")
	}
	return nil
}

// AdmitRepair admits the successor of a failed attempt, once.
func (s *Store) AdmitRepair(ctx context.Context, req RecoveryRequest) (RecoveryAdmission, error) {
	if err := s.writable(); err != nil {
		return RecoveryAdmission{}, err
	}
	if err := req.validate(); err != nil {
		return RecoveryAdmission{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return RecoveryAdmission{}, err
	}
	defer tx.rollback()
	if prior, found, err := priorAdmission(ctx, tx, req); err != nil || found {
		return prior, err
	}
	if err := s.recoveryFences(ctx, tx, req); err != nil {
		return RecoveryAdmission{}, err
	}
	work, err := boundWork(ctx, tx, req)
	if err != nil {
		return RecoveryAdmission{}, err
	}
	for _, g := range work.BudgetGroups {
		res, err := tx.conn.ExecContext(ctx, `UPDATE budget_groups SET repairs_used=repairs_used+1 WHERE id=? AND repairs_used<repair_allowance`, g)
		if err != nil {
			return RecoveryAdmission{}, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return RecoveryAdmission{}, fmt.Errorf("%w: budget group %s of work %s has no automatic repair left", ErrRepairExhausted, g, work.ID)
		}
	}
	res, err := tx.conn.ExecContext(ctx, `UPDATE attempts SET is_current=0, version=version+1 WHERE id=? AND run_id=? AND is_current=1 AND version=?`,
		req.ParentAttemptID, req.RunID, req.ParentVersion)
	if err != nil {
		return RecoveryAdmission{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return RecoveryAdmission{}, fmt.Errorf("%w: attempt %s is not the current attempt at version %d", workflow.ErrStaleAttempt, req.ParentAttemptID, req.ParentVersion)
	}
	if err := s.createAttemptTx(ctx, tx, req.Attempt); err != nil {
		return RecoveryAdmission{}, err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO repair_admissions(id,collaboration_id,logical_work_id,run_id,task_id,parent_attempt_id,successor_attempt_id,
		cause,evidence_hash,original_tree_hash,current_tree_hash,run_version,stop_generation,lease_fence,created_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.ID, req.CollaborationID, work.ID, req.RunID, req.TaskID, req.ParentAttemptID, req.Attempt.ID, req.Cause, req.EvidenceHash,
		req.OriginalTreeHash, req.CurrentTreeHash, req.RunVersion, req.StopGeneration, req.LeaseFence, s.now().UnixMilli()); err != nil {
		if isUnique(err) {
			return RecoveryAdmission{}, fmt.Errorf("%w: attempt %s already has an automatic repair", workflow.ErrConflict, req.ParentAttemptID)
		}
		return RecoveryAdmission{}, err
	}
	for _, g := range work.BudgetGroups {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO repair_admission_groups(admission_id,budget_group_id) VALUES(?,?)`, req.ID, g); err != nil {
			return RecoveryAdmission{}, err
		}
	}
	if err := tx.commit(ctx); err != nil {
		return RecoveryAdmission{}, err
	}
	return RecoveryAdmission{ID: req.ID, SuccessorID: req.Attempt.ID, LogicalWorkID: work.ID, Groups: work.BudgetGroups}, nil
}

// priorAdmission returns the earlier admission for this request, and refuses a
// request that reuses its id for something else.
func priorAdmission(ctx context.Context, tx *dbTx, req RecoveryRequest) (RecoveryAdmission, bool, error) {
	var a RecoveryAdmission
	var parent, cause string
	err := tx.conn.QueryRowContext(ctx, `SELECT successor_attempt_id,logical_work_id,parent_attempt_id,cause FROM repair_admissions WHERE id=?`,
		req.ID).Scan(&a.SuccessorID, &a.LogicalWorkID, &parent, &cause)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	if parent != req.ParentAttemptID || cause != req.Cause || a.SuccessorID != req.Attempt.ID {
		return a, false, fmt.Errorf("%w: repair request %s was admitted for another attempt or cause", workflow.ErrConflict, req.ID)
	}
	rows, err := tx.conn.QueryContext(ctx, `SELECT budget_group_id FROM repair_admission_groups WHERE admission_id=? ORDER BY budget_group_id`, req.ID)
	if err != nil {
		return a, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return a, false, err
		}
		a.Groups = append(a.Groups, g)
	}
	a.ID, a.Duplicate = req.ID, true
	return a, true, rows.Err()
}

// recoveryFences checks the collaboration, run and attempt the request was made
// against. Any one having moved refuses the admission.
func (s *Store) recoveryFences(ctx context.Context, tx *dbTx, req RecoveryRequest) error {
	var status, admission string
	var gen int64
	err := tx.conn.QueryRowContext(ctx, `SELECT status,admission,stop_generation FROM collaborations WHERE id=?`, req.CollaborationID).Scan(&status, &admission, &gen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, req.CollaborationID)
	case err != nil:
		return err
	case status != "open" || admission != "open":
		return fmt.Errorf("%w: collaboration %s is %s with admission %s", ErrStopped, req.CollaborationID, status, admission)
	case gen != req.StopGeneration:
		return fmt.Errorf("%w: the request was made at stop generation %d, now %d", ErrStopped, req.StopGeneration, gen)
	}
	var runState, checkout string
	var pause sql.NullString
	var version int64
	if err := tx.conn.QueryRowContext(ctx, `SELECT r.state,r.pause_code,r.version,p.checkout_id FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`,
		req.RunID).Scan(&runState, &pause, &version, &checkout); errors.Is(err, sql.ErrNoRows) {
		return invalid("no run %s", req.RunID)
	} else if err != nil {
		return err
	}
	// A run that is going on, or one paused by the very failure being repaired.
	// A stop, a budget pause or a source mutation is not a failure to repair.
	failed := runState == "paused" && (pause.String == workflow.PauseStuck || pause.String == workflow.PauseCheckFailed || pause.String == workflow.PauseReviewFailed)
	if runState != "running" && !failed {
		return invalid("run %s is %s (%s) and admits no repair", req.RunID, runState, pause.String)
	}
	if version != req.RunVersion {
		return fmt.Errorf("%w: run %s is at version %d, not %d", workflow.ErrConflict, req.RunID, version, req.RunVersion)
	}
	var owned int
	if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND state='owned' AND fence=? AND expires_ms>?`,
		checkout, req.LeaseFence, s.now().UnixMilli()).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return fmt.Errorf("%w: the writer lease of checkout %s is not held at fence %d", workflow.ErrBusy, checkout, req.LeaseFence)
	}
	var accepted sql.NullString
	if err := tx.conn.QueryRowContext(ctx, `SELECT accepted_attempt_id FROM tasks WHERE run_id=? AND id=?`, req.RunID, req.TaskID).Scan(&accepted); err != nil {
		return invalid("run %s has no task %s", req.RunID, req.TaskID)
	}
	if accepted.Valid {
		return fmt.Errorf("%w: task %s is already accepted", workflow.ErrConflict, req.TaskID)
	}
	return nil
}

// boundWork is the active logical work the task is bound to. A task with no
// binding, or bound to work that was split or merged away, cannot be repaired
// automatically: which allowance it draws on needs a person.
func boundWork(ctx context.Context, tx *dbTx, req RecoveryRequest) (LogicalWork, error) {
	var id string
	err := tx.conn.QueryRowContext(ctx, `SELECT logical_work_id FROM work_task_bindings WHERE run_id=? AND task_id=?`, req.RunID, req.TaskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return LogicalWork{}, fmt.Errorf("%w: task %s of run %s is bound to no logical work", ErrWorkMapping, req.TaskID, req.RunID)
	}
	if err != nil {
		return LogicalWork{}, err
	}
	w, err := workTx(ctx, tx.conn, req.CollaborationID, id)
	if err != nil {
		return w, err
	}
	if w.State != "active" {
		return w, fmt.Errorf("%w: work %s was %s; bind the task to its successor", ErrWorkMapping, w.ID, w.State)
	}
	if len(w.BudgetGroups) == 0 {
		return w, fmt.Errorf("%w: work %s carries no budget group", ErrWorkMapping, w.ID)
	}
	sort.Strings(w.BudgetGroups)
	return w, nil
}

// RepairAdmissionFor returns the admission made for a failed attempt.
func (s *Store) RepairAdmissionFor(ctx context.Context, parentAttemptID string) (RecoveryAdmission, bool, error) {
	var a RecoveryAdmission
	err := s.db.QueryRowContext(ctx, `SELECT id,successor_attempt_id,logical_work_id FROM repair_admissions WHERE parent_attempt_id=?`,
		parentAttemptID).Scan(&a.ID, &a.SuccessorID, &a.LogicalWorkID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	a.Duplicate = true
	return a, err == nil, err
}
