package state

import (
	"context"
	"database/sql"
	"errors"
)

// RunStatus is the run a checkout is currently working on.
type RunStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	PauseCode string `json:"pause_code,omitempty"`
	Version   int64  `json:"version"`
}

// UsageStatus says how complete the recorded token usage is. Completeness is
// "none" with no records, "complete" when every record is complete, and
// "partial" when any record is partial or unavailable: an unknown count is
// never folded into a total.
type UsageStatus struct {
	Records      int    `json:"records"`
	Complete     int    `json:"complete"`
	Partial      int    `json:"partial"`
	Unavailable  int    `json:"unavailable"`
	Completeness string `json:"completeness"`
}

// Report reads a checkout's current run and usage completeness. It only
// queries; it is safe on a read-only Store and changes nothing.
func (s *Store) Report(ctx context.Context, checkoutID string) (*RunStatus, UsageStatus, error) {
	var run *RunStatus
	r := RunStatus{}
	var pause sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT r.id,r.state,r.pause_code,r.version FROM runs r JOIN projects p ON p.id=r.project_id
		WHERE p.checkout_id=? AND r.state IN ('running','paused','stopping') ORDER BY r.created_ms DESC, r.id DESC LIMIT 1`, checkoutID).
		Scan(&r.ID, &r.State, &pause, &r.Version)
	switch {
	case err == nil:
		r.PauseCode = pause.String
		run = &r
	case !errors.Is(err, sql.ErrNoRows):
		return nil, UsageStatus{}, err
	}

	u := UsageStatus{Completeness: "none"}
	rows, err := s.db.QueryContext(ctx, `SELECT u.completeness,count(*) FROM usage_records u JOIN attempts a ON a.id=u.attempt_id
		JOIN runs r ON r.id=a.run_id JOIN projects p ON p.id=r.project_id WHERE p.checkout_id=? GROUP BY u.completeness`, checkoutID)
	if err != nil {
		return run, u, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return run, u, err
		}
		u.Records += n
		switch kind {
		case "complete":
			u.Complete = n
		case "partial":
			u.Partial = n
		case "unavailable":
			u.Unavailable = n
		}
	}
	if u.Records > 0 {
		u.Completeness = "complete"
		if u.Partial > 0 || u.Unavailable > 0 {
			u.Completeness = "partial"
		}
	}
	return run, u, rows.Err()
}
