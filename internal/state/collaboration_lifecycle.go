package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Closing a collaboration, and what collection reads about its sources.
// BeginClose freezes launches first: a closing collaboration registers and
// admits nothing new, so no paid closeout can start after the freeze, while
// collection, settlement and reporting go on. MarkClosed ends it once a final
// snapshot is complete; until then it stays closing with a partial snapshot.

// Segment is a planner's ownership of a session, between two message ids.
type Segment struct {
	SourceID, CollaborationID, StartAfter, EndThrough string
	Closed                                            bool
}

// BeginClose moves an open collaboration to closing. Repeating it is a no-op.
func (s *Store) BeginClose(ctx context.Context, id string) error {
	return s.closeStep(ctx, id, "open", "closing")
}

// MarkClosed moves a closing collaboration to closed.
func (s *Store) MarkClosed(ctx context.Context, id string) error {
	return s.closeStep(ctx, id, "closing", "closed")
}

func (s *Store) closeStep(ctx context.Context, id, from, to string) error {
	if err := s.writable(); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	status, err := s.collaborationExists(ctx, tx, id)
	if err != nil {
		return err
	}
	switch {
	case status == to || (to == "closing" && status == "closed"):
		return nil
	case status != from:
		return invalid("collaboration %s is %s, not %s", id, status, from)
	}
	closedMS := sql.NullInt64{}
	if to == "closed" {
		closedMS = sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE collaborations SET status=?, closed_ms=COALESCE(?,closed_ms), version=version+1 WHERE id=?`,
		to, closedMS, id); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// ListSources returns a collaboration's sources in the order they were
// registered.
func (s *Store) ListSources(ctx context.Context, collaborationID string) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM expected_sources WHERE collaboration_id=? ORDER BY created_ms, id`, collaborationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// SessionSegments returns every collaboration's segments of one session, so
// an event two collaborations claim is found ambiguous.
func (s *Store) SessionSegments(ctx context.Context, provider, sessionID string) ([]Segment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id,collaboration_id,COALESCE(start_after,''),COALESCE(end_through,''),closed_at IS NOT NULL
		FROM session_segments WHERE provider=? AND session_id=? ORDER BY opened_ms, id`, provider, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Segment
	for rows.Next() {
		var g Segment
		if err := rows.Scan(&g.SourceID, &g.CollaborationID, &g.StartAfter, &g.EndThrough, &g.Closed); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MarkSourceTerminal records that a bound source's work has ended, for a
// source no run job settles (a registered preflight reviewer, for one). Its
// log must still end in terminal evidence for its usage to be final.
func (s *Store) MarkSourceTerminal(ctx context.Context, id string) error {
	if err := s.writable(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE expected_sources SET status='terminal' WHERE id=? AND status IN ('bound','terminal')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Source(ctx, id); err != nil {
			return err
		}
		return invalid("source %s has no bound session to end", id)
	}
	return nil
}

// JobSettled reports whether a job has ended: completed, failed or cancelled.
func (s *Store) JobSettled(ctx context.Context, jobID string) (bool, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: no job %s", ErrNoSource, jobID)
	}
	return state == "completed" || state == "failed" || state == "cancelled", err
}

// AttemptJobsSettled reports whether an attempt has jobs of a kind and all of
// them have ended.
func (s *Store) AttemptJobsSettled(ctx context.Context, attemptID, kind string) (bool, error) {
	var total, open int
	err := s.db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(state NOT IN ('completed','failed','cancelled')),0)
		FROM jobs WHERE attempt_id=? AND kind=?`, attemptID, kind).Scan(&total, &open)
	return total > 0 && open == 0, err
}

// CollaborationCreatedMS returns when the collaboration was created.
func (s *Store) CollaborationCreatedMS(ctx context.Context, id string) (int64, error) {
	var ms int64
	err := s.db.QueryRowContext(ctx, `SELECT created_ms FROM collaborations WHERE id=?`, id).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: no collaboration %s", ErrWorkMapping, id)
	}
	return ms, err
}
