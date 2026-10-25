package state

import (
	"context"
	"database/sql"
)

// EventRow is one applied event as the log holds it.
type EventRow struct {
	Seq            int64
	ID             string
	AttemptID      string
	Kind           string
	PayloadHash    string
	AppliedVersion int64
	CreatedMS      int64
}

// RunEvents lists a run's applied events in the order they were applied.
func (s *Store) RunEvents(ctx context.Context, runID string) ([]EventRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,id,attempt_id,kind,payload_hash,applied_version,created_ms FROM events WHERE run_id=? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var r EventRow
		var attempt sql.NullString
		if err := rows.Scan(&r.Seq, &r.ID, &attempt, &r.Kind, &r.PayloadHash, &r.AppliedVersion, &r.CreatedMS); err != nil {
			return nil, err
		}
		r.AttemptID = attempt.String
		out = append(out, r)
	}
	return out, rows.Err()
}
