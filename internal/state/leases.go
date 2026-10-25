package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// LeaseInfo describes a checkout's writer lease.
type LeaseInfo struct {
	Owner, State string
	Fence        int64
	ExpiresMS    int64
}

// AcquireLease makes owner the checkout's one writer and returns the fence.
// The owner may renew its own lease. Any other caller gets workflow.ErrBusy:
// while the lease is live because someone is writing, and once it has expired
// because its writer's state is unknown, which turns the lease to reconciling.
// An expired lease is never taken over; it is released after reconciliation.
func (s *Store) AcquireLease(ctx context.Context, checkoutID, owner string, ttl time.Duration) (int64, error) {
	if owner == "" || !idPattern.MatchString(checkoutID) {
		return 0, fmt.Errorf("%w: a lease needs an owner and a checkout id", workflow.ErrInvalidTransition)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.rollback()
	fence, err := s.leaseTx(ctx, tx, checkoutID, owner, ttl)
	if errors.Is(err, errLeaseFlipped) {
		// The flip to reconciling is the point: keep it, and still refuse.
		if cerr := tx.commit(ctx); cerr != nil {
			return 0, cerr
		}
		return 0, fmt.Errorf("%w: the writer lease for checkout %s expired and is being reconciled", workflow.ErrBusy, checkoutID)
	}
	if err != nil {
		return 0, err
	}
	return fence, tx.commit(ctx)
}

var errLeaseFlipped = errors.New("lease turned to reconciling")

// leaseTx is AcquireLease inside a caller's transaction. An expired lease held
// by someone else is turned to reconciling and reported as errLeaseFlipped,
// which the caller may commit or discard; other refusals are workflow.ErrBusy.
func (s *Store) leaseTx(ctx context.Context, tx *dbTx, checkoutID, owner string, ttl time.Duration) (int64, error) {
	now := s.now().UnixMilli()
	expires := now + ttl.Milliseconds()
	var cur LeaseInfo
	err := tx.conn.QueryRowContext(ctx, `SELECT owner_id,state,fence,expires_ms FROM checkout_leases WHERE checkout_id=?`, checkoutID).
		Scan(&cur.Owner, &cur.State, &cur.Fence, &cur.ExpiresMS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO checkout_leases(checkout_id,owner_id,expires_ms,fence,state) VALUES(?,?,?,1,'owned')`, checkoutID, owner, expires); err != nil {
			return 0, err
		}
		return 1, nil
	case err != nil:
		return 0, err
	}
	switch {
	case cur.State == "released":
		if _, err := tx.conn.ExecContext(ctx, `UPDATE checkout_leases SET owner_id=?,expires_ms=?,fence=fence+1,state='owned' WHERE checkout_id=?`, owner, expires, checkoutID); err != nil {
			return 0, err
		}
		return cur.Fence + 1, nil
	case cur.State == "owned" && cur.Owner == owner:
		if _, err := tx.conn.ExecContext(ctx, `UPDATE checkout_leases SET expires_ms=? WHERE checkout_id=?`, expires, checkoutID); err != nil {
			return 0, err
		}
		return cur.Fence, nil
	case cur.State == "owned" && cur.ExpiresMS <= now:
		// The writer's lease ran out: what it was doing is unknown.
		if _, err := tx.conn.ExecContext(ctx, `UPDATE checkout_leases SET state='reconciling' WHERE checkout_id=?`, checkoutID); err != nil {
			return 0, err
		}
		return 0, errLeaseFlipped
	}
	return 0, fmt.Errorf("%w: the writer lease for checkout %s is held by %s (%s)", workflow.ErrBusy, checkoutID, cur.Owner, cur.State)
}

// LeaseOwnedBy reports whether owner holds a live, owned lease at this fence.
func (s *Store) LeaseOwnedBy(ctx context.Context, checkoutID, owner string, fence int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND owner_id=? AND fence=? AND state='owned' AND expires_ms>?`,
		checkoutID, owner, fence, s.now().UnixMilli()).Scan(&n)
	return n == 1, err
}

// LeaseState reads a checkout's lease; the zero value means none.
func (s *Store) LeaseState(ctx context.Context, checkoutID string) (LeaseInfo, error) {
	var l LeaseInfo
	err := s.db.QueryRowContext(ctx, `SELECT owner_id,state,fence,expires_ms FROM checkout_leases WHERE checkout_id=?`, checkoutID).Scan(&l.Owner, &l.State, &l.Fence, &l.ExpiresMS)
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseInfo{}, nil
	}
	return l, err
}

// ReleaseLease frees the lease its owner holds. The owner and the fence must
// both match.
func (s *Store) ReleaseLease(ctx context.Context, checkoutID, owner string, fence int64) error {
	l, err := s.LeaseState(ctx, checkoutID)
	if err != nil {
		return err
	}
	switch {
	case l.Fence != fence:
		return staleFence(checkoutID, fence, l.Fence)
	case l.Owner != owner:
		return fmt.Errorf("%w: %s is not the holder of checkout %s's lease", workflow.ErrConflict, owner, checkoutID)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE checkout_leases SET state='released' WHERE checkout_id=? AND fence=? AND owner_id=?`, checkoutID, fence, owner)
	return err
}

// BreakLease frees a lease that has gone to reconciling, once the controller
// has established that nothing is still writing. It refuses a live lease.
func (s *Store) BreakLease(ctx context.Context, checkoutID string, fence int64) error {
	l, err := s.LeaseState(ctx, checkoutID)
	if err != nil {
		return err
	}
	switch {
	case l.Fence != fence:
		return staleFence(checkoutID, fence, l.Fence)
	case l.State != "reconciling":
		return fmt.Errorf("%w: the lease for %s is %s, not reconciling", workflow.ErrConflict, checkoutID, l.State)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE checkout_leases SET state='released' WHERE checkout_id=? AND fence=?`, checkoutID, fence)
	return err
}

// acquires one.
func (s *Store) RenewLease(ctx context.Context, checkoutID, owner string, fence int64, ttl time.Duration) error {
	res, err := s.db.ExecContext(ctx, `UPDATE checkout_leases SET expires_ms=? WHERE checkout_id=? AND owner_id=? AND fence=? AND state='owned'`,
		s.now().Add(ttl).UnixMilli(), checkoutID, owner, fence)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: %s no longer holds the lease for checkout %s at fence %d", ErrStaleFence, owner, checkoutID, fence)
	}
	return nil
}
