package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// CollaborationRequest creates a collaboration: the identity above runs that
// spans planning, every run and replacement run, reviews and closeout.
//
// A ceiling is a per-provider scheduling limit; a provider missing from a map
// has no ceiling declared on that side. Unlimited providers are declared
// explicitly. A reservation is what one launch for that provider reserves.
// With EnforceBudgets, every launch must be admitted by AdmitDispatch, and a
// provider with no declared budget is refused rather than treated as
// unlimited. Without it, launches are registered and usage collected, but
// nothing is reserved.
type CollaborationRequest struct {
	ID, PolicyHash                        string
	InputCeilings, OutputCeilings         map[string]int64
	EnforceBudgets                        bool
	Unlimited                             []string
	InputReservations, OutputReservations map[string]int64
}

// budgetRow is one provider's declared budget.
type budgetRow struct {
	Unlimited                           bool
	InputCeiling, OutputCeiling         sql.NullInt64
	InputReservation, OutputReservation sql.NullInt64
}

func (s *Store) writable() error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{workflow.ErrInvalidTransition}, args...)...)
}

func declaredBudgets(req CollaborationRequest) (map[string]budgetRow, error) {
	rows := map[string]budgetRow{}
	set := func(m map[string]int64, apply func(*budgetRow, int64)) error {
		for p, v := range m {
			if p == "" || v < 0 {
				return invalid("%q=%d: providers are named and budgets are not negative", p, v)
			}
			r := rows[p]
			apply(&r, v)
			rows[p] = r
		}
		return nil
	}
	for _, step := range []struct {
		m     map[string]int64
		apply func(*budgetRow, int64)
	}{
		{req.InputCeilings, func(r *budgetRow, v int64) { r.InputCeiling = sql.NullInt64{Int64: v, Valid: true} }},
		{req.OutputCeilings, func(r *budgetRow, v int64) { r.OutputCeiling = sql.NullInt64{Int64: v, Valid: true} }},
		{req.InputReservations, func(r *budgetRow, v int64) { r.InputReservation = sql.NullInt64{Int64: v, Valid: true} }},
		{req.OutputReservations, func(r *budgetRow, v int64) { r.OutputReservation = sql.NullInt64{Int64: v, Valid: true} }},
	} {
		if err := set(step.m, step.apply); err != nil {
			return nil, err
		}
	}
	for _, p := range req.Unlimited {
		r := rows[p]
		if p == "" || r.InputCeiling.Valid || r.OutputCeiling.Valid {
			return nil, invalid("provider %q is declared unlimited and given a ceiling", p)
		}
		r.Unlimited = true
		rows[p] = r
	}
	return rows, nil
}

// CreateCollaboration records an open collaboration, its provider budgets and
// a reference to its policy artifact. Repeating an identical request succeeds;
// a different request under the same id is ErrConflict.
func (s *Store) CreateCollaboration(ctx context.Context, req CollaborationRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if !idPattern.MatchString(req.ID) || !hashPattern.MatchString(req.PolicyHash) {
		return invalid("a collaboration needs a 32 hex character id and a policy artifact hash")
	}
	budgets, err := declaredBudgets(req)
	if err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var policy string
	var enforced bool
	switch err := tx.conn.QueryRowContext(ctx, `SELECT policy_hash,budget_enforced FROM collaborations WHERE id=?`, req.ID).Scan(&policy, &enforced); {
	case err == nil:
		stored, err := budgetsTx(ctx, tx, req.ID)
		if err != nil {
			return err
		}
		if policy != req.PolicyHash || enforced != req.EnforceBudgets || fmt.Sprint(stored) != fmt.Sprint(budgets) {
			return fmt.Errorf("%w: collaboration %s exists with another policy or budgets", workflow.ErrConflict, req.ID)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO collaborations(id,policy_hash,status,budget_enforced,created_ms) VALUES(?,?,'open',?,?)`,
		req.ID, req.PolicyHash, req.EnforceBudgets, s.now().UnixMilli()); err != nil {
		if isForeignKey(err) {
			return invalid("policy %s is not a stored artifact", req.PolicyHash)
		}
		return err
	}
	for p, b := range budgets {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO provider_budgets(collaboration_id,provider,unlimited,input_ceiling,output_ceiling,
			input_reservation,output_reservation) VALUES(?,?,?,?,?,?,?)`, req.ID, p, b.Unlimited, b.InputCeiling, b.OutputCeiling,
			b.InputReservation, b.OutputReservation); err != nil {
			return err
		}
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('collaboration',?,'policy',?)`,
		req.ID, req.PolicyHash); err != nil {
		return err
	}
	return tx.commit(ctx)
}

func budgetsTx(ctx context.Context, tx *dbTx, id string) (map[string]budgetRow, error) {
	rows, err := tx.conn.QueryContext(ctx, `SELECT provider,unlimited,input_ceiling,output_ceiling,input_reservation,output_reservation
		FROM provider_budgets WHERE collaboration_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]budgetRow{}
	for rows.Next() {
		var p string
		var b budgetRow
		if err := rows.Scan(&p, &b.Unlimited, &b.InputCeiling, &b.OutputCeiling, &b.InputReservation, &b.OutputReservation); err != nil {
			return nil, err
		}
		out[p] = b
	}
	return out, rows.Err()
}

// requireOpen fails unless the collaboration exists and is open to new work.
func requireOpen(ctx context.Context, tx *dbTx, id string) error {
	var status string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT status FROM collaborations WHERE id=?`, id).Scan(&status); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, id)
	case err != nil:
		return err
	case status != "open":
		return invalid("collaboration %s is %s and takes no new work mappings", id, status)
	}
	return nil
}

// requireLaunchable fails unless the collaboration is open and its admission
// has not been stopped: a launch is registered only where it could be admitted.
// A closing or closed collaboration is frozen: like a stopped one, it holds
// the launch rather than failing it.
func requireLaunchable(ctx context.Context, tx *dbTx, id string) error {
	var status, admission string
	err := tx.conn.QueryRowContext(ctx, `SELECT status,admission FROM collaborations WHERE id=?`, id).Scan(&status, &admission)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, id)
	case err != nil:
		return err
	case status != "open":
		return fmt.Errorf("%w: collaboration %s is %s and admits no launch", ErrStopped, id, status)
	case admission != "open":
		return fmt.Errorf("%w: collaboration %s is stopped; an audited resume reopens it", ErrStopped, id)
	}
	return nil
}

// CollaborationStatus returns open, closing or closed, or ErrWorkMapping for an
// unknown collaboration.
func (s *Store) CollaborationStatus(ctx context.Context, id string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM collaborations WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, id)
	}
	return status, err
}
