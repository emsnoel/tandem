package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// After admission: settling usage, releasing what was certainly not sent, and
// the audited controls a person uses on a collaboration. Stopping is always
// available, at a ceiling too; collection and archival do not pass admission.

// ControlRequest is a person's stop or resume, kept in the audit trail.
type ControlRequest struct{ CollaborationID, Actor, Reason string }

// AmendRequest changes a provider's ceilings. Only an explicit, audited
// amendment raises an allowance; replanning never does.
type AmendRequest struct {
	CollaborationID, Provider   string
	InputCeiling, OutputCeiling *int64
	Actor, Reason               string
}

func audit(ctx context.Context, tx *dbTx, collab, action, provider, detail, actor, reason string, now int64) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.conn.ExecContext(ctx, `INSERT INTO collaboration_audit(id,collaboration_id,action,provider,detail,actor,reason,stop_generation,created_ms)
		SELECT ?,id,?,?,?,?,?,stop_generation,? FROM collaborations WHERE id=?`, id, action, nullable(provider), detail, actor, reason, now, collab)
	return err
}

// SettleAdmission moves an admission's reservation to spend once its final
// usage is known, and records any overshoot past a ceiling. With unknown usage
// the reservation stays outstanding. Settling again with the same usage is a
// no-op; with other usage it is ErrConflict.
func (s *Store) SettleAdmission(ctx context.Context, req SettleRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if (req.InputUsed != nil && *req.InputUsed < 0) || (req.OutputUsed != nil && *req.OutputUsed < 0) {
		return invalid("usage is not negative")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var collab, provider, state string
	var inRes, outRes int64
	var inUsed, outUsed sql.NullInt64
	err = tx.conn.QueryRowContext(ctx, `SELECT collaboration_id,provider,state,input_reserved,output_reserved,input_used,output_used
		FROM dispatch_admissions WHERE id=?`, req.AdmissionID).Scan(&collab, &provider, &state, &inRes, &outRes, &inUsed, &outUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no admission %s", ErrNoSource, req.AdmissionID)
	} else if err != nil {
		return err
	}
	known := req.InputUsed != nil && req.OutputUsed != nil
	switch state {
	case "settled":
		if known && inUsed.Int64 == *req.InputUsed && outUsed.Int64 == *req.OutputUsed {
			return nil
		}
		return fmt.Errorf("%w: admission %s is settled with other usage", workflow.ErrConflict, req.AdmissionID)
	case "released":
		return fmt.Errorf("%w: admission %s was released", workflow.ErrConflict, req.AdmissionID)
	}
	if !known {
		if _, err := tx.conn.ExecContext(ctx, `UPDATE dispatch_admissions SET state='unknown_tail' WHERE id=?`, req.AdmissionID); err != nil {
			return err
		}
		return tx.commit(ctx)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE provider_budgets SET input_reserved=input_reserved-?, output_reserved=output_reserved-?,
		input_spent=input_spent+?, output_spent=output_spent+? WHERE collaboration_id=? AND provider=?`,
		inRes, outRes, *req.InputUsed, *req.OutputUsed, collab, provider); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE provider_budgets SET
		input_overshoot=MAX(input_overshoot, CASE WHEN unlimited=0 AND input_ceiling IS NOT NULL THEN input_spent-input_ceiling ELSE 0 END),
		output_overshoot=MAX(output_overshoot, CASE WHEN unlimited=0 AND output_ceiling IS NOT NULL THEN output_spent-output_ceiling ELSE 0 END)
		WHERE collaboration_id=? AND provider=?`, collab, provider); err != nil {
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE dispatch_admissions SET state='settled', input_used=?, output_used=?, settled_ms=? WHERE id=?`,
		*req.InputUsed, *req.OutputUsed, s.now().UnixMilli(), req.AdmissionID); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// InFlightAdmission is an admission whose usage is not yet settled.
type InFlightAdmission struct {
	ID, SourceID   string
	StopGeneration int64
}

// InFlightAdmissions lists admissions whose usage is not yet settled, in the
// order they were admitted. A stop leaves them here: it cannot recall a send.
func (s *Store) InFlightAdmissions(ctx context.Context, collaborationID string) ([]InFlightAdmission, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,source_id,stop_generation FROM dispatch_admissions
		WHERE collaboration_id=? AND state IN ('reserved','unknown_tail') ORDER BY created_ms, id`, collaborationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InFlightAdmission
	for rows.Next() {
		var a InFlightAdmission
		if err := rows.Scan(&a.ID, &a.SourceID, &a.StopGeneration); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func needsActor(req ControlRequest) error {
	if req.Actor == "" || req.Reason == "" {
		return invalid("a stop or resume names who did it and why")
	}
	return nil
}

// StopCollaboration stops admission and increments the stop generation in one
// transaction. It is always available, closing collaborations included.
// Stopping a stopped collaboration is a no-op.
func (s *Store) StopCollaboration(ctx context.Context, req ControlRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if err := needsActor(req); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	res, err := tx.conn.ExecContext(ctx, `UPDATE collaborations SET admission='stopped', stop_generation=stop_generation+1, version=version+1
		WHERE id=? AND admission='open'`, req.CollaborationID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.collaborationExists(ctx, tx, req.CollaborationID); err != nil {
			return err
		}
		return nil
	}
	if err := audit(ctx, tx, req.CollaborationID, "stop", "", "{}", req.Actor, req.Reason, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// ResumeCollaboration reopens a stopped, still open collaboration's admission.
// The generation is not reset, so nothing computed before the stop is admitted.
func (s *Store) ResumeCollaboration(ctx context.Context, req ControlRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if err := needsActor(req); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	status, err := s.collaborationExists(ctx, tx, req.CollaborationID)
	if err != nil {
		return err
	}
	if status != "open" {
		return invalid("collaboration %s is %s; closing never resumes", req.CollaborationID, status)
	}
	res, err := tx.conn.ExecContext(ctx, `UPDATE collaborations SET admission='open', version=version+1 WHERE id=? AND admission='stopped'`,
		req.CollaborationID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := audit(ctx, tx, req.CollaborationID, "resume", "", "{}", req.Actor, req.Reason, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.commit(ctx)
}

func (s *Store) collaborationExists(ctx context.Context, tx *dbTx, id string) (string, error) {
	var status string
	err := tx.conn.QueryRowContext(ctx, `SELECT status FROM collaborations WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, id)
	}
	return status, err
}

// AmendBudget changes a provider's ceilings with an audit row naming who and
// why. An unlimited provider takes no ceiling.
func (s *Store) AmendBudget(ctx context.Context, req AmendRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	switch {
	case req.Actor == "" || req.Reason == "":
		return invalid("a budget amendment names who made it and why")
	case req.Provider == "" || (req.InputCeiling == nil && req.OutputCeiling == nil):
		return invalid("an amendment names a provider and at least one ceiling")
	case (req.InputCeiling != nil && *req.InputCeiling < 0) || (req.OutputCeiling != nil && *req.OutputCeiling < 0):
		return invalid("ceilings are not negative")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	before, err := budgetTx(ctx, tx.conn, req.CollaborationID, req.Provider)
	if err != nil {
		return err
	}
	if before.Unlimited {
		return invalid("provider %s is declared unlimited", req.Provider)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE provider_budgets SET input_ceiling=COALESCE(?,input_ceiling), output_ceiling=COALESCE(?,output_ceiling)
		WHERE collaboration_id=? AND provider=?`, req.InputCeiling, req.OutputCeiling, req.CollaborationID, req.Provider); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"input_ceiling": [2]*int64{before.InputCeiling, req.InputCeiling},
		"output_ceiling": [2]*int64{before.OutputCeiling, req.OutputCeiling}})
	if err := audit(ctx, tx, req.CollaborationID, "amend_budget", req.Provider, string(detail), req.Actor, req.Reason, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// SettledSessionUsage is what admissions of sources on one provider session
// have already settled, so later admissions on it settle only the remainder.
func (s *Store) SettledSessionUsage(ctx context.Context, collaborationID, provider, sessionID string) (input, output int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(a.input_used),0), COALESCE(sum(a.output_used),0)
		FROM dispatch_admissions a JOIN expected_sources s ON s.id=a.source_id
		WHERE a.collaboration_id=? AND a.state='settled' AND s.provider=? AND s.session_id=?`,
		collaborationID, provider, sessionID).Scan(&input, &output)
	return input, output, err
}
