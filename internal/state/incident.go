package state

// Recovery incidents: the persistent key that makes one failure one decision.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Incident is one unresolved or resolved failure the policy did not repair
// automatically.
type Incident struct {
	Key, CollaborationID, RunID, TaskID, AttemptID, LogicalWorkID string
	Kind, Reason, EvidenceHash, State, DecisionEffectID           string
	Resolution                                                    string
}

// IncidentSpec opens an incident. Key is derived by the caller from the
// collaboration, the work and the failure generation.
type IncidentSpec struct {
	Key, CollaborationID, RunID, TaskID, AttemptID, LogicalWorkID string
	Kind, Reason, EvidenceHash                                    string
}

const incidentColumns = `key,collaboration_id,run_id,task_id,attempt_id,COALESCE(logical_work_id,''),kind,reason,COALESCE(evidence_hash,''),state,COALESCE(decision_effect_id,''),COALESCE(resolution,'')`

func scanIncident(row interface{ Scan(...any) error }) (Incident, error) {
	var i Incident
	err := row.Scan(&i.Key, &i.CollaborationID, &i.RunID, &i.TaskID, &i.AttemptID, &i.LogicalWorkID, &i.Kind, &i.Reason, &i.EvidenceHash,
		&i.State, &i.DecisionEffectID, &i.Resolution)
	return i, err
}

// OpenIncident records an incident, once per key. created reports whether this
// call made it; a repeat returns the stored row unchanged, so a duplicate
// failure event cannot open a second decision.
func (s *Store) OpenIncident(ctx context.Context, spec IncidentSpec) (Incident, bool, error) {
	if err := s.writable(); err != nil {
		return Incident{}, false, err
	}
	if !idPattern.MatchString(spec.Key) || spec.Kind == "" || spec.Reason == "" {
		return Incident{}, false, invalid("an incident needs a 32 hex character key, a kind and a reason")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Incident{}, false, err
	}
	defer tx.rollback()
	if prior, err := scanIncident(tx.conn.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM recovery_incidents WHERE key=?`, spec.Key)); err == nil {
		return prior, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Incident{}, false, err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO recovery_incidents(key,collaboration_id,run_id,task_id,attempt_id,logical_work_id,kind,reason,evidence_hash,state,created_ms)
		VALUES(?,?,?,?,?,?,?,?,?,'open',?)`, spec.Key, spec.CollaborationID, spec.RunID, spec.TaskID, spec.AttemptID, nullable(spec.LogicalWorkID),
		spec.Kind, spec.Reason, nullable(spec.EvidenceHash), s.now().UnixMilli()); err != nil {
		if isForeignKey(err) {
			return Incident{}, false, invalid("incident %s names a collaboration, run, task or attempt that does not exist", spec.Key)
		}
		return Incident{}, false, err
	}
	i, err := scanIncident(tx.conn.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM recovery_incidents WHERE key=?`, spec.Key))
	if err != nil {
		return Incident{}, false, err
	}
	return i, true, tx.commit(ctx)
}

// NoteIncidentNotified records the decision request that was queued for an
// open incident. Noting the same effect again succeeds; another effect for the
// same incident is a conflict, because one incident asks one question.
func (s *Store) NoteIncidentNotified(ctx context.Context, key, effectID string) error {
	if err := s.writable(); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	i, err := scanIncident(tx.conn.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM recovery_incidents WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no incident %s", workflow.ErrConflict, key)
	}
	if err != nil {
		return err
	}
	switch {
	case i.State == "notified" && i.DecisionEffectID == effectID:
		return nil
	case i.State != "open":
		return fmt.Errorf("%w: incident %s is %s", workflow.ErrConflict, key, i.State)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE recovery_incidents SET state='notified', decision_effect_id=?, notified_ms=? WHERE key=? AND state='open'`,
		effectID, s.now().UnixMilli(), key); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// ResolveIncident closes an incident with the reason it is closed. Resolving
// twice with the same resolution succeeds.
func (s *Store) ResolveIncident(ctx context.Context, key, resolution string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if resolution == "" {
		return invalid("an incident is resolved with a reason")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE recovery_incidents SET state='resolved', resolution=?, resolved_ms=? WHERE key=? AND state<>'resolved'`,
		resolution, s.now().UnixMilli(), key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var got string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(resolution,'') FROM recovery_incidents WHERE key=?`, key).Scan(&got); err != nil || got != resolution {
			return fmt.Errorf("%w: incident %s cannot be resolved as %q", workflow.ErrConflict, key, resolution)
		}
	}
	return nil
}

// RunIncidents lists a run's incidents, oldest first.
func (s *Store) RunIncidents(ctx context.Context, runID string) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+incidentColumns+` FROM recovery_incidents WHERE run_id=? ORDER BY created_ms, key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RepairBudget is the automatic repair allowance of one budget group.
type RepairBudget struct {
	GroupID         string
	Allowance, Used int
	LogicalWorkID   string
}

// WorkRepairBudgets returns the repair allowance and use of every budget group
// the work draws on, for a decision request.
func (s *Store) WorkRepairBudgets(ctx context.Context, collaborationID, workID string) ([]RepairBudget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.repair_allowance,g.repairs_used FROM work_budget_groups w JOIN budget_groups g ON g.id=w.budget_group_id
		WHERE w.logical_work_id=? AND g.collaboration_id=? ORDER BY g.id`, workID, collaborationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepairBudget
	for rows.Next() {
		b := RepairBudget{LogicalWorkID: workID}
		if err := rows.Scan(&b.GroupID, &b.Allowance, &b.Used); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// TaskWork returns the logical work a run's task is bound to, or "".
func (s *Store) TaskWork(ctx context.Context, runID, taskID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT logical_work_id FROM work_task_bindings WHERE run_id=? AND task_id=?`, runID, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// BudgetProviders lists the providers a collaboration declares a budget for.
func (s *Store) BudgetProviders(ctx context.Context, collaborationID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider FROM provider_budgets WHERE collaboration_id=? ORDER BY provider`, collaborationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
