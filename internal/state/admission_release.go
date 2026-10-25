package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// ReleaseAdmission returns a reservation whose effect certainly delivered
// nothing: it failed its last permitted send or was cancelled before sending.
// An unknown delivery keeps its reservation (ErrDeliveryUnknown).
func (s *Store) ReleaseAdmission(ctx context.Context, admissionID string) error {
	if err := s.writable(); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var collab, provider, state, effectState string
	var inRes, outRes int64
	err = tx.conn.QueryRowContext(ctx, `SELECT a.collaboration_id,a.provider,a.state,a.input_reserved,a.output_reserved,COALESCE(e.state,'')
		FROM dispatch_admissions a LEFT JOIN effects e ON e.id=a.effect_id WHERE a.id=?`, admissionID).
		Scan(&collab, &provider, &state, &inRes, &outRes, &effectState)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no admission %s", ErrNoSource, admissionID)
	case err != nil:
		return err
	case state == "released":
		return nil
	case state != "reserved":
		return invalid("admission %s is %s; only a reservation that was never used can be released", admissionID, state)
	case effectState == "delivery_unknown":
		return fmt.Errorf("%w: the effect of admission %s may have been delivered", workflow.ErrDeliveryUnknown, admissionID)
	case effectState != "failed" && effectState != "cancelled":
		return invalid("the effect of admission %s is %q, not certainly undelivered", admissionID, effectState)
	}
	if err := releaseReservation(ctx, tx, s.now().UnixMilli(), reservation{admissionID, collab, provider, inRes, outRes}); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// reservation is one reserved admission: what it holds against which budget.
type reservation struct {
	ID, Collab, Provider string
	In, Out              int64
}

// releaseReservation returns r's reservation to its provider budget and marks
// the admission released, inside tx. It is the one place a reservation is
// released; the caller has already shown its effect delivered nothing.
func releaseReservation(ctx context.Context, tx *dbTx, nowMS int64, r reservation) error {
	if _, err := tx.conn.ExecContext(ctx, `UPDATE provider_budgets SET input_reserved=input_reserved-?, output_reserved=output_reserved-?
		WHERE collaboration_id=? AND provider=?`, r.In, r.Out, r.Collab, r.Provider); err != nil {
		return err
	}
	_, err := tx.conn.ExecContext(ctx, `UPDATE dispatch_admissions SET state='released', settled_ms=? WHERE id=? AND state='reserved'`, nowMS, r.ID)
	return err
}

// releaseEffectAdmissions releases every reserved admission of effectID,
// inside tx. The caller has already shown the effect delivered nothing.
func releaseEffectAdmissions(ctx context.Context, tx *dbTx, nowMS int64, effectID string) error {
	rows, err := tx.conn.QueryContext(ctx, `SELECT id,collaboration_id,provider,input_reserved,output_reserved
		FROM dispatch_admissions WHERE effect_id=? AND state='reserved'`, effectID)
	if err != nil {
		return err
	}
	var rs []reservation
	for rows.Next() {
		var r reservation
		if err := rows.Scan(&r.ID, &r.Collab, &r.Provider, &r.In, &r.Out); err != nil {
			rows.Close()
			return err
		}
		rs = append(rs, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rs {
		if err := releaseReservation(ctx, tx, nowMS, r); err != nil {
			return err
		}
	}
	return nil
}
