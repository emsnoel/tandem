package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Effect timing, from the delivery contract.
const (
	// LeaseDuration is how long a claim holds an effect; a worker renews it
	// every HeartbeatInterval while it works.
	LeaseDuration     = 30 * time.Second
	HeartbeatInterval = 10 * time.Second
	// AckWindow is how long a prompt the transport accepted may wait for the
	// builder's nonce-bound acknowledgement before the effect is treated as
	// unconfirmed.
	AckWindow = 10 * time.Minute
	// ReconcileDelay spaces repeated reconciliation of one unknown effect.
	ReconcileDelay = 30 * time.Second
	// MaxSendFailures is how many definite not-sent failures an effect gets.
	MaxSendFailures = 3
)

var (
	// ErrNoEffect: nothing is claimable now.
	ErrNoEffect = errors.New("no effect to deliver")
	// ErrStaleFence: a write carried a fence that is no longer the effect's.
	// It also matches workflow.ErrConflict.
	ErrStaleFence = errors.New("stale fence")
)

func staleFence(id string, got, want int64) error {
	return fmt.Errorf("%w; %w: effect %s is at fence %d, the write carried %d", ErrStaleFence, workflow.ErrConflict, id, want, got)
}

// backoff is the delay before retrying after the n-th definite failure: 1 s,
// 2 s, then 4 s.
func backoff(failures int) time.Duration {
	d := time.Second << (failures - 1)
	return min(d, 4*time.Second)
}

const effectColumns = `id,run_id,attempt_id,job_id,kind,payload_hash,nonce,state,fence`

func scanEffect(row interface{ Scan(...any) error }) (workflow.Effect, error) {
	var e workflow.Effect
	var attempt, job sql.NullString
	err := row.Scan(&e.ID, &e.RunID, &attempt, &job, &e.Kind, &e.PayloadHash, &e.Nonce, &e.State, &e.Fence)
	e.AttemptID, e.JobID = attempt.String, job.String
	return e, err
}

// GetEffect reads one effect.
func (s *Store) GetEffect(ctx context.Context, id string) (workflow.Effect, error) {
	e, err := scanEffect(s.db.QueryRowContext(ctx, `SELECT `+effectColumns+` FROM effects WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, fmt.Errorf("%w: effect %s does not exist", workflow.ErrConflict, id)
	}
	return e, err
}

// ClaimEffect hands one effect to owner under a lease, committing the claim
// before the caller does any external I/O. The fence rises on every claim, and
// every later write names it, so a worker that was paused past its lease cannot
// record anything after another has claimed.
//
// What it returns says what the owner may do:
//   - state delivering: a pending effect, to be sent (first or retried);
//   - state delivery_unknown: an effect whose send may already have happened,
//     either because an earlier send was ambiguous or because its lease expired
//     while a worker held it. The owner reconciles by observing; it never
//     sends again.
//
// Order: unknown effects due for reconciliation, expired leases (which become
// unknown), then pending effects that are available. While a builder dispatch
// in a run is unknown, no further builder dispatch in that run is claimed: the
// writer's state is not known. ErrNoEffect when nothing qualifies.
func (s *Store) ClaimEffect(ctx context.Context, owner, runID string) (workflow.Effect, error) {
	return s.claimEffect(ctx, owner, runID, nil)
}

// ClaimEffectKinds is ClaimEffect restricted to the given effect kinds, so the
// worker that sends prompts and the one that runs local steps never take each
// other's effects. An empty list is refused rather than meaning every kind.
func (s *Store) ClaimEffectKinds(ctx context.Context, owner, runID string, kinds []string) (workflow.Effect, error) {
	if len(kinds) == 0 {
		return workflow.Effect{}, fmt.Errorf("%w: a kind-restricted claim needs at least one kind", workflow.ErrInvalidTransition)
	}
	return s.claimEffect(ctx, owner, runID, kinds)
}

func (s *Store) claimEffect(ctx context.Context, owner, runID string, kinds []string) (workflow.Effect, error) {
	if owner == "" {
		return workflow.Effect{}, fmt.Errorf("%w: a claim needs an owner", workflow.ErrInvalidTransition)
	}
	if runID == "" {
		return workflow.Effect{}, fmt.Errorf("%w: a claim needs a run", workflow.ErrInvalidTransition)
	}
	// A tick drives one run, so it claims only that run's effects; runs in one
	// checkout share this store.
	kindFilter, kindArgs := " AND run_id=?", []any{runID}
	if len(kinds) > 0 {
		marks := strings.Repeat("?,", len(kinds))
		kindFilter += " AND kind IN (" + marks[:len(marks)-1] + ")"
		for _, k := range kinds {
			kindArgs = append(kindArgs, k)
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return workflow.Effect{}, err
	}
	defer tx.rollback()
	now := s.now().UnixMilli()

	pick := func(query string, args ...any) (string, error) {
		var id string
		err := tx.conn.QueryRowContext(ctx, query, args...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return id, err
	}
	id, err := pick(`SELECT id FROM effects WHERE state='delivery_unknown' AND available_ms<=? AND (lease_until_ms IS NULL OR lease_until_ms<=?)`+kindFilter+`
		ORDER BY created_ms,id LIMIT 1`, append([]any{now, now}, kindArgs...)...)
	if err != nil {
		return workflow.Effect{}, err
	}
	next := "delivery_unknown"
	if id == "" {
		if id, err = pick(`SELECT id FROM effects WHERE state='delivering' AND lease_until_ms<=?`+kindFilter+` ORDER BY created_ms,id LIMIT 1`, append([]any{now}, kindArgs...)...); err != nil {
			return workflow.Effect{}, err
		}
	}
	if id == "" {
		next = "delivering"
		if id, err = pick(`SELECT e.id FROM effects e WHERE e.state='pending' AND e.available_ms<=?
			AND NOT (e.kind=? AND EXISTS (SELECT 1 FROM effects u WHERE u.run_id=e.run_id AND u.kind=? AND u.state='delivery_unknown'))`+strings.NewReplacer("kind IN", "e.kind IN", "run_id=", "e.run_id=").Replace(kindFilter)+`
			ORDER BY e.created_ms,e.id LIMIT 1`, append([]any{now, workflow.EffectBuilderDispatch, workflow.EffectBuilderDispatch}, kindArgs...)...); err != nil {
			return workflow.Effect{}, err
		}
	}
	if id == "" {
		return workflow.Effect{}, ErrNoEffect
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state=?, lease_owner=?, lease_until_ms=?, fence=fence+1 WHERE id=?`,
		next, owner, now+LeaseDuration.Milliseconds(), id); err != nil {
		return workflow.Effect{}, err
	}
	eff, err := scanEffect(tx.conn.QueryRowContext(ctx, `SELECT `+effectColumns+` FROM effects WHERE id=?`, id))
	if err != nil {
		return workflow.Effect{}, err
	}
	return eff, tx.commit(ctx)
}

// fenced runs fn on an effect whose fence matches, inside one transaction.
func (s *Store) fenced(ctx context.Context, id string, fence int64, fn func(tx *dbTx, state string, now int64) error) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var state string
	var current int64
	switch err := tx.conn.QueryRowContext(ctx, `SELECT state,fence FROM effects WHERE id=?`, id).Scan(&state, &current); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: effect %s does not exist", workflow.ErrConflict, id)
	case err != nil:
		return err
	}
	if current != fence {
		return staleFence(id, fence, current)
	}
	if err := fn(tx, state, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.commit(ctx)
}

func wrongState(id, state, want string) error {
	return fmt.Errorf("%w: effect %s is %s, expected %s", workflow.ErrConflict, id, state, want)
}

// RecordDelivery stores the transport's receipt for an effect the caller still
// holds. It does not acknowledge: a receipt says the transport accepted the
// request, not that the agent read it. The effect stays delivering and waits
// for its nonce for AckWindow. An effect the agent already acknowledged while
// the send was being confirmed keeps that state and only gains the receipt.
func (s *Store) RecordDelivery(ctx context.Context, effectID string, fence int64, receiptHash string) error {
	if !hashPattern.MatchString(receiptHash) {
		return fmt.Errorf("%w: malformed receipt hash %q", workflow.ErrInvalidTransition, receiptHash)
	}
	return s.fenced(ctx, effectID, fence, func(tx *dbTx, state string, now int64) error {
		switch state {
		case "delivering":
			if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET receipt_hash=?, lease_until_ms=? WHERE id=?`,
				receiptHash, now+AckWindow.Milliseconds(), effectID); err != nil {
				return err
			}
		case "acknowledged":
			if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET receipt_hash=COALESCE(receipt_hash, ?) WHERE id=?`,
				receiptHash, effectID); err != nil {
				return err
			}
		default:
			return wrongState(effectID, state, "delivering")
		}
		_, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('effect',?,'receipt',?)`, effectID, receiptHash)
		return err
	})
}

// RecordNotSent records a failure that is certain not to have sent anything.
// The effect returns to pending after a backoff with the same id, until it has
// failed MaxSendFailures times, then it is failed.
func (s *Store) RecordNotSent(ctx context.Context, effectID string, fence int64, reasonHash string) error {
	if reasonHash != "" && !hashPattern.MatchString(reasonHash) {
		return fmt.Errorf("%w: malformed reason hash %q", workflow.ErrInvalidTransition, reasonHash)
	}
	return s.fenced(ctx, effectID, fence, func(tx *dbTx, state string, now int64) error {
		if state != "delivering" {
			return wrongState(effectID, state, "delivering")
		}
		var failures int
		if err := tx.conn.QueryRowContext(ctx, `SELECT retry_count FROM effects WHERE id=?`, effectID).Scan(&failures); err != nil {
			return err
		}
		failures++
		if failures >= MaxSendFailures {
			_, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='failed', retry_count=?, not_sent_hash=COALESCE(?, not_sent_hash), lease_owner=NULL, lease_until_ms=NULL WHERE id=?`,
				failures, nullable(reasonHash), effectID)
			return err
		}
		_, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='pending', retry_count=?, not_sent_hash=COALESCE(?, not_sent_hash), available_ms=?, lease_owner=NULL, lease_until_ms=NULL WHERE id=?`,
			failures, nullable(reasonHash), now+backoff(failures).Milliseconds(), effectID)
		return err
	})
}

// ReleaseClaim returns a claimed effect to pending without counting a failed
// send: nothing was attempted because the launch is held, by a stopped
// collaboration or an exhausted budget. Its retries are untouched.
func (s *Store) ReleaseClaim(ctx context.Context, effectID string, fence int64) error {
	return s.fenced(ctx, effectID, fence, func(tx *dbTx, state string, now int64) error {
		if state != "delivering" {
			return wrongState(effectID, state, "delivering")
		}
		_, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='pending', lease_owner=NULL, lease_until_ms=NULL WHERE id=?`, effectID)
		return err
	})
}

// MarkDeliveryUnknown records that a send may or may not have happened. The
// retry counter is not touched: an unknown send is never retried, only
// reconciled. evidenceHash, when not empty, names an artifact holding the
// transport's evidence (its error text, session ids) and is retained.
func (s *Store) MarkDeliveryUnknown(ctx context.Context, effectID string, fence int64, evidenceHash string) error {
	if evidenceHash != "" && !hashPattern.MatchString(evidenceHash) {
		return fmt.Errorf("%w: malformed evidence hash %q", workflow.ErrInvalidTransition, evidenceHash)
	}
	return s.fenced(ctx, effectID, fence, func(tx *dbTx, state string, now int64) error {
		if state != "delivering" {
			return wrongState(effectID, state, "delivering")
		}
		if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='delivery_unknown', receipt_hash=COALESCE(?, receipt_hash), available_ms=?, lease_owner=NULL, lease_until_ms=NULL WHERE id=?`,
			nullable(evidenceHash), now, effectID); err != nil {
			return err
		}
		if evidenceHash == "" {
			return nil
		}
		_, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('effect',?,'evidence',?)`, effectID, evidenceHash)
		return err
	})
}

// ReleaseReconcile ends a reconciliation that learned nothing: the effect stays
// delivery_unknown and is offered again after ReconcileDelay.
func (s *Store) ReleaseReconcile(ctx context.Context, effectID string, fence int64) error {
	return s.fenced(ctx, effectID, fence, func(tx *dbTx, state string, now int64) error {
		if state != "delivery_unknown" {
			return wrongState(effectID, state, "delivery_unknown")
		}
		_, err := tx.conn.ExecContext(ctx, `UPDATE effects SET available_ms=?, lease_owner=NULL, lease_until_ms=NULL WHERE id=?`,
			now+ReconcileDelay.Milliseconds(), effectID)
		return err
	})
}

// HeartbeatEffect extends the lease of an effect the caller still holds.
func (s *Store) HeartbeatEffect(ctx context.Context, effectID string, fence int64) error {
	return s.fenced(ctx, effectID, fence, func(tx *dbTx, state string, now int64) error {
		if state != "delivering" && state != "delivery_unknown" {
			return wrongState(effectID, state, "delivering")
		}
		_, err := tx.conn.ExecContext(ctx, `UPDATE effects SET lease_until_ms=? WHERE id=?`, now+LeaseDuration.Milliseconds(), effectID)
		return err
	})
}

// AcknowledgeEffect resolves a delivery. Only the effect's own nonce does it:
// the nonce travels in the prompt and comes back in the builder's first
// action, so a started process, a transport receipt or a lookalike session
// cannot confirm a prompt that was never read. Repeating it is harmless.
func (s *Store) AcknowledgeEffect(ctx context.Context, effectID, nonce string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var state, want string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT state,nonce FROM effects WHERE id=?`, effectID).Scan(&state, &want); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: effect %s does not exist", workflow.ErrConflict, effectID)
	case err != nil:
		return err
	}
	if nonce != want {
		return fmt.Errorf("%w: the acknowledgement does not carry effect %s's nonce", workflow.ErrConflict, effectID)
	}
	switch state {
	case "acknowledged":
		return nil
	case "delivering", "delivery_unknown":
	default:
		return wrongState(effectID, state, "delivering or delivery_unknown")
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE effects SET state='acknowledged', lease_owner=NULL, lease_until_ms=NULL WHERE id=?`, effectID); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// EffectNotSentReason returns the artifact holding why an effect was last
// definitely not sent, or false when it never was.
func (s *Store) EffectNotSentReason(ctx context.Context, effectID string) (string, bool, error) {
	var h sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT not_sent_hash FROM effects WHERE id=?`, effectID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !h.Valid) {
		return "", false, nil
	}
	return h.String, err == nil, err
}
