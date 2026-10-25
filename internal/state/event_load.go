package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

func validateEvent(ev workflow.Event) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: invalid event: %s", workflow.ErrInvalidTransition, fmt.Sprintf(format, a...))
	}
	switch {
	case !idPattern.MatchString(ev.ID):
		return bad("id %q is not 32 lowercase hex characters", ev.ID)
	case !idPattern.MatchString(ev.RunID):
		return bad("run id %q is not 32 lowercase hex characters", ev.RunID)
	case ev.AttemptID != "" && !idPattern.MatchString(ev.AttemptID):
		return bad("attempt id %q is not 32 lowercase hex characters", ev.AttemptID)
	case !hashPattern.MatchString(ev.PayloadHash):
		return bad("payload hash %q is not 64 lowercase hex characters", ev.PayloadHash)
	case ev.ExpectedVersion < 0:
		return bad("negative expected version %d", ev.ExpectedVersion)
	}
	for _, k := range workflow.AllEventKinds {
		if k == ev.Kind {
			return nil
		}
	}
	return bad("unknown kind %q", ev.Kind)
}

// duplicate answers a replay. The identity compared is kind, run, attempt and
// payload; the expected version is not, because a retry may be issued after the
// attempt has moved on. The receipt is the original one.
func (s *Store) duplicate(ctx context.Context, conn *sql.Conn, ev workflow.Event) (workflow.Receipt, bool, error) {
	var runID, kind, payload string
	var attemptID sql.NullString
	var applied int64
	err := conn.QueryRowContext(ctx, `SELECT run_id,attempt_id,kind,payload_hash,applied_version FROM events WHERE id=?`, ev.ID).
		Scan(&runID, &attemptID, &kind, &payload, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Receipt{}, false, nil
	}
	if err != nil {
		return workflow.Receipt{}, false, err
	}
	if runID != ev.RunID || attemptID.String != ev.AttemptID || kind != ev.Kind || payload != ev.PayloadHash {
		return workflow.Receipt{}, false, fmt.Errorf("%w: event %s was already applied with different content", workflow.ErrConflict, ev.ID)
	}
	return workflow.Receipt{EventID: ev.ID, Version: applied, Duplicate: true}, true, nil
}

func loadRun(ctx context.Context, conn *sql.Conn, runID string) (version int64, state string, err error) {
	err = conn.QueryRowContext(ctx, `SELECT version,state FROM runs WHERE id=?`, runID).Scan(&version, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("%w: run %s does not exist", workflow.ErrConflict, runID)
	}
	return version, state, err
}

// loadAttempt reads the attempt and checks the event belongs to its run.
func loadAttempt(ctx context.Context, conn *sql.Conn, ev workflow.Event) (workflow.Attempt, error) {
	var a workflow.Attempt
	var parent, start, end, patch, packet sql.NullString
	var accepted sql.NullInt64
	var current int
	err := conn.QueryRowContext(ctx, `SELECT id,run_id,task_id,parent_attempt_id,ordinal,implementation_state,check_state,review_state,budget_state,
		start_snapshot_hash,end_snapshot_hash,patch_hash,packet_hash,version,is_current,accepted_ms FROM attempts WHERE id=? AND run_id=?`,
		ev.AttemptID, ev.RunID).Scan(&a.ID, &a.RunID, &a.TaskID, &parent, &a.Ordinal, &a.Implementation, &a.Check, &a.Review, &a.Budget,
		&start, &end, &patch, &packet, &a.Version, &current, &accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: attempt %s is not part of run %s", workflow.ErrConflict, ev.AttemptID, ev.RunID)
	}
	if err != nil {
		return a, err
	}
	a.ParentAttemptID, a.StartSnapshotHash, a.EndSnapshotHash = parent.String, start.String, end.String
	a.PatchHash, a.PacketHash, a.Current = patch.String, packet.String, current == 1
	if accepted.Valid {
		a.AcceptedMS = &accepted.Int64
	}
	return a, nil
}

func updateAttempt(ctx context.Context, conn *sql.Conn, was, now workflow.Attempt) error {
	current := 0
	if now.Current {
		current = 1
	}
	var accepted any
	if now.AcceptedMS != nil {
		accepted = *now.AcceptedMS
	}
	res, err := conn.ExecContext(ctx, `UPDATE attempts SET implementation_state=?,check_state=?,review_state=?,budget_state=?,version=?,is_current=?,accepted_ms=?
		WHERE id=? AND version=?`, now.Implementation, now.Check, now.Review, now.Budget, now.Version, current, accepted, was.ID, was.Version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: attempt %s changed under the transaction", workflow.ErrConflict, was.ID)
	}
	return nil
}

// AcceptanceFacts loads the facts acceptance is decided on for an attempt, as
// an accept_requested event would see them now. It only reads, so it works on
// a store opened read-only.
func (s *Store) AcceptanceFacts(ctx context.Context, a workflow.Attempt, obs workflow.Observed) (workflow.Facts, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return workflow.Facts{}, mapBusy(err)
	}
	defer conn.Close()
	return s.loadFacts(ctx, conn, a, workflow.Event{RunID: a.RunID, AttemptID: a.ID, Kind: workflow.EventAcceptRequested}, obs)
}

// loadFacts builds the trusted facts from persisted rows plus the controller's
// observations. A fact the database cannot prove is false.
func (s *Store) loadFacts(ctx context.Context, conn *sql.Conn, a workflow.Attempt, ev workflow.Event, obs workflow.Observed) (workflow.Facts, error) {
	f := workflow.Facts{
		// Membership and currency were checked before this point.
		IdentityValid:        ev.AttemptID != "",
		SessionSettled:       obs.SessionSettled,
		WriterOwned:          obs.WriterOwned,
		SchedulingAllowed:    obs.SchedulingAllowed,
		NoCheckPolicy:        obs.NoCheckPolicy,
		NoReviewPolicy:       obs.NoReviewPolicy,
		VerificationRequired: obs.RequireVerification,
		Budget:               obs.Budget,
		NowMS:                obs.NowMS,
		RepairAllowed:        a.Ordinal < MaxAttempts,
	}
	if f.Budget == "" {
		f.Budget = a.Budget
	}
	if f.NowMS == 0 {
		f.NowMS = s.now().UnixMilli()
	}
	if ev.AttemptID == "" {
		return f, nil
	}
	// A recorded hash is the controller's attestation that the capture was
	// complete; incomplete captures are never given a hash.
	f.SnapshotComplete = a.StartSnapshotHash != "" && (ev.Kind == workflow.EventAttemptCreated || a.EndSnapshotHash != "")
	f.PatchComplete = a.PatchHash != ""

	var open int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM task_dependencies td JOIN tasks p ON p.run_id=td.run_id AND p.id=td.predecessor_id
		WHERE td.run_id=? AND td.task_id=? AND p.accepted_attempt_id IS NULL`, a.RunID, a.TaskID).Scan(&open); err != nil {
		return f, err
	}
	f.DependenciesAccepted = open == 0

	var exit int
	var complete int
	var before, after, jobState string
	err := conn.QueryRowContext(ctx, `SELECT c.exit_code,c.complete,c.before_inputs_hash,c.after_inputs_hash,j.state FROM check_certificates c
		JOIN jobs j ON j.id=c.job_id WHERE c.attempt_id=? AND j.kind='check' ORDER BY c.created_ms DESC, c.id DESC LIMIT 1`, a.ID).
		Scan(&exit, &complete, &before, &after, &jobState)
	switch {
	case err == nil:
		// Content-bound: the check saw one set of inputs and left it unchanged.
		f.CheckCertificateValid = complete == 1 && before == after && jobState == "completed"
		f.CheckPassed = exit == 0
	case !errors.Is(err, sql.ErrNoRows):
		return f, err
	}

	var status, patch, tree string
	// The builder's review. A result of the independent verification stage is
	// another stage's evidence and is never read as this one.
	err = conn.QueryRowContext(ctx, `SELECT r.status,r.patch_hash,r.tree_hash,j.state FROM review_results r JOIN jobs j ON j.id=r.job_id
		WHERE r.attempt_id=? AND j.kind='reviewer'
		AND NOT EXISTS (SELECT 1 FROM review_stages s WHERE s.job_id=r.job_id AND s.stage='independent_verification')
		ORDER BY r.created_ms DESC, r.job_id DESC LIMIT 1`, a.ID).Scan(&status, &patch, &tree, &jobState)
	switch {
	case err == nil:
		// A review counts only for the patch and tree it examined.
		f.ReviewResultValid = jobState == "completed" && patch == a.PatchHash && tree == a.EndSnapshotHash && a.PatchHash != ""
		f.ReviewStatus = workflow.ReviewState(status)
	case !errors.Is(err, sql.ErrNoRows):
		return f, err
	}

	if f.VerificationRequired {
		// The reviewer pane's review is the independent check. Without a review
		// from it, the planner's verification still is.
		v, err := reviewerOrVerification(ctx, conn, a)
		if err != nil {
			return f, err
		}
		f.VerificationValid, f.VerificationStatus = v.Valid, v.Status
	}

	rows, err := conn.QueryContext(ctx, `SELECT scope,evidence_tree_hash FROM waivers WHERE attempt_id=?`, a.ID)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	allMatch, any := true, false
	for rows.Next() {
		var scope, tree string
		if err := rows.Scan(&scope, &tree); err != nil {
			return f, err
		}
		any = true
		allMatch = allMatch && tree == a.EndSnapshotHash && a.EndSnapshotHash != ""
		switch scope {
		case "implementation":
			f.ImplementationWaiver = true
		case "check":
			f.CheckWaiver = true
		case "review":
			f.ReviewWaiver = true
		}
	}
	f.WaiverTreeMatches = any && allMatch
	return f, rows.Err()
}

// mapBusy turns SQLite's lock timeout into workflow.ErrBusy.
func mapBusy(err error) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked") {
		return fmt.Errorf("%w: %v", workflow.ErrBusy, err)
	}
	return err
}

// GetAttempt reads one attempt.
func (s *Store) GetAttempt(ctx context.Context, id string) (workflow.Attempt, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return workflow.Attempt{}, mapBusy(err)
	}
	defer conn.Close()
	var runID string
	if err := conn.QueryRowContext(ctx, `SELECT run_id FROM attempts WHERE id=?`, id).Scan(&runID); errors.Is(err, sql.ErrNoRows) {
		return workflow.Attempt{}, fmt.Errorf("%w: attempt %s does not exist", workflow.ErrConflict, id)
	} else if err != nil {
		return workflow.Attempt{}, err
	}
	return loadAttempt(ctx, conn, workflow.Event{RunID: runID, AttemptID: id})
}

// GetJob reads one job, or reports that it does not exist.
func (s *Store) GetJob(ctx context.Context, id string) (workflow.Job, bool, error) {
	var j workflow.Job
	var patch, tree, result, session sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,attempt_id,kind,state,input_hash,config_hash,patch_hash,tree_hash,result_hash,runner_kind,session_id FROM jobs WHERE id=?`, id).
		Scan(&j.ID, &j.AttemptID, &j.Kind, &j.State, &j.InputHash, &j.ConfigHash, &patch, &tree, &result, &j.RunnerKind, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	j.PatchHash, j.TreeHash, j.ResultHash, j.SessionID = patch.String, tree.String, result.String, session.String
	return j, err == nil, err
}
