package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Dispatch admission. In a collaboration that enforces budgets, every model
// launch is admitted here before anything is sent. One IMMEDIATE transaction
// checks the collaboration's stop generation, the run and its version, the
// current attempt, the checkout lease, the registered source and both of the
// provider's ceilings, then records the admission and its reservation. A stop
// increments the generation in its own transaction, so whichever commits first
// wins: an admission before the stop stays recorded and in flight even if the
// send happens later, and nothing computed before the stop is admitted after
// it. A stop cannot recall an admitted send.

var (
	// ErrStopped: the collaboration's admission is stopped or frozen, or the
	// request was computed at an older stop generation.
	ErrStopped = errors.New("collaboration admission is stopped")
	// ErrBudgetExhausted: spent plus reserved plus the request exceeds a ceiling.
	ErrBudgetExhausted = errors.New("collaboration budget exhausted")
	// ErrBudgetUnconfigured: an enforced collaboration declares no budget for
	// the provider. It is never treated as unlimited.
	ErrBudgetUnconfigured = errors.New("no budget declared for this provider")
	// ErrNotEnforced: the collaboration does not enforce budgets, so launches
	// are registered without admission.
	ErrNotEnforced = errors.New("collaboration does not enforce budgets")
)

// AdmissionRequest asks to send one effect. RunID, AttemptID and LeaseFence
// are checked when RunID is set; a launch outside any run (planning, closeout)
// leaves them empty.
type AdmissionRequest struct {
	ID, CollaborationID, RunID, AttemptID, JobID, EffectID string
	SourceID, Provider, BudgetGroupID                      string
	RunVersion, StopGeneration, LeaseFence                 int64
	InputReservation, OutputReservation                    int64
}

// Admission is a durable permission to send one effect.
type Admission struct {
	ID             string
	StopGeneration int64
}

// SettleRequest records a launch's final usage. A nil counter is unknown: the
// reservation is kept, so it can never be spent twice.
type SettleRequest struct {
	AdmissionID           string
	InputUsed, OutputUsed *int64
}

// ProviderBudget is one provider's budget in a collaboration.
type ProviderBudget struct {
	Unlimited                           bool
	InputCeiling, OutputCeiling         *int64
	InputReservation, OutputReservation *int64
	InputSpent, OutputSpent             int64
	InputReserved, OutputReserved       int64
	InputOvershoot, OutputOvershoot     int64
}

type admitHooks struct {
	// beforeCommit runs inside AdmitDispatch's transaction, just before COMMIT.
	beforeCommit func()
}

// reservationFits reports whether requested more tokens fit under limit with
// spent and reserved already counted. Any negative input does not fit.
func reservationFits(limit, spent, reserved, requested int64) bool {
	if limit < 0 || spent < 0 || reserved < 0 || requested < 0 {
		return false
	}
	if spent > limit || reserved > limit-spent {
		return false
	}
	return requested <= limit-spent-reserved
}

func ptr64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

func budgetTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, collab, provider string) (ProviderBudget, error) {
	var b ProviderBudget
	var inC, outC, inR, outR sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT unlimited,input_ceiling,output_ceiling,input_reservation,output_reservation,
		input_spent,output_spent,input_reserved,output_reserved,input_overshoot,output_overshoot
		FROM provider_budgets WHERE collaboration_id=? AND provider=?`, collab, provider).Scan(&b.Unlimited, &inC, &outC, &inR, &outR,
		&b.InputSpent, &b.OutputSpent, &b.InputReserved, &b.OutputReserved, &b.InputOvershoot, &b.OutputOvershoot)
	if errors.Is(err, sql.ErrNoRows) {
		return b, fmt.Errorf("%w: %s in collaboration %s", ErrBudgetUnconfigured, provider, collab)
	}
	b.InputCeiling, b.OutputCeiling, b.InputReservation, b.OutputReservation = ptr64(inC), ptr64(outC), ptr64(inR), ptr64(outR)
	return b, err
}

// ProviderBudget returns a provider's budget, or ErrBudgetUnconfigured.
func (s *Store) ProviderBudget(ctx context.Context, collaborationID, provider string) (ProviderBudget, error) {
	return budgetTx(ctx, s.db, collaborationID, provider)
}

// StopGeneration returns the collaboration's current stop generation.
func (s *Store) StopGeneration(ctx context.Context, collaborationID string) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(ctx, `SELECT stop_generation FROM collaborations WHERE id=?`, collaborationID).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, collaborationID)
	}
	return gen, err
}

// AdmitDispatch admits one effect, or refuses with the first fence it fails.
// An identical request returns the same admission; a changed one, or a second
// admission for the same effect, is ErrConflict. Nothing is written on refusal.
func (s *Store) AdmitDispatch(ctx context.Context, req AdmissionRequest) (Admission, error) {
	if err := s.writable(); err != nil {
		return Admission{}, err
	}
	if !idPattern.MatchString(req.ID) || req.CollaborationID == "" || req.EffectID == "" || req.SourceID == "" || req.Provider == "" ||
		req.InputReservation < 0 || req.OutputReservation < 0 || (req.AttemptID != "" && req.RunID == "") {
		return Admission{}, invalid("an admission names its id, collaboration, effect, source and provider, with non-negative reservations")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Admission{}, err
	}
	defer tx.rollback()
	if prior, found, err := admissionTx(ctx, tx, req.ID, req.EffectID); err != nil {
		return Admission{}, err
	} else if found {
		if prior != req {
			return Admission{}, fmt.Errorf("%w: effect %s or admission %s is already admitted differently", workflow.ErrConflict, req.EffectID, req.ID)
		}
		return Admission{ID: req.ID, StopGeneration: req.StopGeneration}, nil
	}
	if err := checkFences(ctx, tx, req, s.now().UnixMilli()); err != nil {
		return Admission{}, err
	}
	b, err := budgetTx(ctx, tx.conn, req.CollaborationID, req.Provider)
	if err != nil {
		return Admission{}, err
	}
	if !b.Unlimited {
		if b.InputCeiling == nil || b.OutputCeiling == nil {
			return Admission{}, fmt.Errorf("%w: %s declares an input or output ceiling but not both", ErrBudgetUnconfigured, req.Provider)
		}
		if !reservationFits(*b.InputCeiling, b.InputSpent, b.InputReserved, req.InputReservation) ||
			!reservationFits(*b.OutputCeiling, b.OutputSpent, b.OutputReserved, req.OutputReservation) {
			return Admission{}, fmt.Errorf("%w: %s has spent %d/%d and reserved %d/%d input/output against %d/%d; %d/%d requested",
				ErrBudgetExhausted, req.Provider, b.InputSpent, b.OutputSpent, b.InputReserved, b.OutputReserved,
				*b.InputCeiling, *b.OutputCeiling, req.InputReservation, req.OutputReservation)
		}
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO dispatch_admissions(id,collaboration_id,effect_id,source_id,provider,budget_group_id,
		run_id,attempt_id,job_id,run_version,stop_generation,lease_fence,input_reserved,output_reserved,state,created_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'reserved',?)`, req.ID, req.CollaborationID, req.EffectID, req.SourceID, req.Provider,
		nullable(req.BudgetGroupID), nullable(req.RunID), nullable(req.AttemptID), nullable(req.JobID), req.RunVersion, req.StopGeneration,
		req.LeaseFence, req.InputReservation, req.OutputReservation, s.now().UnixMilli()); err != nil {
		return Admission{}, err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE provider_budgets SET input_reserved=input_reserved+?, output_reserved=output_reserved+?
		WHERE collaboration_id=? AND provider=?`, req.InputReservation, req.OutputReservation, req.CollaborationID, req.Provider); err != nil {
		return Admission{}, err
	}
	if s.admitHooks.beforeCommit != nil {
		s.admitHooks.beforeCommit()
	}
	if err := tx.commit(ctx); err != nil {
		return Admission{}, err
	}
	return Admission{ID: req.ID, StopGeneration: req.StopGeneration}, nil
}

// admissionTx loads an admission stored under id or for effectID.
func admissionTx(ctx context.Context, tx *dbTx, id, effectID string) (AdmissionRequest, bool, error) {
	var r AdmissionRequest
	err := tx.conn.QueryRowContext(ctx, `SELECT id,collaboration_id,COALESCE(run_id,''),COALESCE(attempt_id,''),COALESCE(job_id,''),effect_id,
		source_id,provider,COALESCE(budget_group_id,''),run_version,stop_generation,lease_fence,input_reserved,output_reserved
		FROM dispatch_admissions WHERE id=? OR effect_id=? LIMIT 1`, id, effectID).Scan(&r.ID, &r.CollaborationID, &r.RunID, &r.AttemptID,
		&r.JobID, &r.EffectID, &r.SourceID, &r.Provider, &r.BudgetGroupID, &r.RunVersion, &r.StopGeneration, &r.LeaseFence,
		&r.InputReservation, &r.OutputReservation)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// checkFences is every admission check except the budget arithmetic.
func checkFences(ctx context.Context, tx *dbTx, req AdmissionRequest, now int64) error {
	var status, admission string
	var enforced bool
	var gen int64
	err := tx.conn.QueryRowContext(ctx, `SELECT status,admission,budget_enforced,stop_generation FROM collaborations WHERE id=?`,
		req.CollaborationID).Scan(&status, &admission, &enforced, &gen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, req.CollaborationID)
	case err != nil:
		return err
	case !enforced:
		return fmt.Errorf("%w: %s", ErrNotEnforced, req.CollaborationID)
	case status != "open" || admission != "open":
		return fmt.Errorf("%w: collaboration %s is %s with admission %s", ErrStopped, req.CollaborationID, status, admission)
	case gen != req.StopGeneration:
		return fmt.Errorf("%w: the request was made at stop generation %d, now %d", ErrStopped, req.StopGeneration, gen)
	}
	if req.RunID != "" {
		var state, checkout string
		var version int64
		if err := tx.conn.QueryRowContext(ctx, `SELECT r.state,r.version,p.checkout_id FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`,
			req.RunID).Scan(&state, &version, &checkout); errors.Is(err, sql.ErrNoRows) {
			return invalid("no run %s", req.RunID)
		} else if err != nil {
			return err
		}
		if state != "running" {
			return invalid("run %s is %s and dispatches nothing", req.RunID, state)
		}
		if version != req.RunVersion {
			return fmt.Errorf("%w: run %s is at version %d, not %d", workflow.ErrConflict, req.RunID, version, req.RunVersion)
		}
		if req.AttemptID != "" {
			var current int
			if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM attempts WHERE id=? AND run_id=? AND is_current=1`,
				req.AttemptID, req.RunID).Scan(&current); err != nil {
				return err
			}
			if current != 1 {
				return fmt.Errorf("%w: attempt %s is not the current attempt of run %s", workflow.ErrStaleAttempt, req.AttemptID, req.RunID)
			}
		}
		var owned int
		if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND state='owned' AND fence=? AND expires_ms>?`,
			checkout, req.LeaseFence, now).Scan(&owned); err != nil {
			return err
		}
		if owned != 1 {
			return fmt.Errorf("%w: the writer lease of checkout %s is not held at fence %d", workflow.ErrBusy, checkout, req.LeaseFence)
		}
	}
	src, err := sourceTx(ctx, tx.conn, req.SourceID)
	if err != nil {
		return err
	}
	if src.CollaborationID != req.CollaborationID || src.Status == "abandoned" {
		return invalid("source %s belongs to collaboration %s and is %s", src.ID, src.CollaborationID, src.Status)
	}
	if req.BudgetGroupID != "" {
		var n int
		if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM budget_groups WHERE id=? AND collaboration_id=?`,
			req.BudgetGroupID, req.CollaborationID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: no budget group %s in collaboration %s", ErrWorkMapping, req.BudgetGroupID, req.CollaborationID)
		}
	}
	return nil
}

// BudgetEnforced reports whether launches in the collaboration need admission.
func (s *Store) BudgetEnforced(ctx context.Context, collaborationID string) (bool, error) {
	var enforced bool
	err := s.db.QueryRowContext(ctx, `SELECT budget_enforced FROM collaborations WHERE id=?`, collaborationID).Scan(&enforced)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, collaborationID)
	}
	return enforced, err
}
