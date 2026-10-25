package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Expected sources: every model job a collaboration pays for is registered
// here before its launch is sent, so collection can tell a missing log from
// usage that never happened. A launch receipt then binds the session the
// provider actually used. A source without a bound session is unresolved and
// keeps the collaboration's accounting incomplete; nothing is attached by
// repository or time proximity.

// ErrNoSource is matched by an error for a source id that was never registered.
var ErrNoSource = errors.New("no such expected source")

// SourceRequest registers one expected source. CollaborationID is the launch
// owner; a child (ParentSourceID set) inherits its parent's and may not name
// another. A planner names its SessionID and the message it starts after
// (StartBoundary), and opens an exclusive session segment.
type SourceRequest struct {
	ID, CollaborationID, Provider, Model, Role         string
	SessionID, ParentSourceID, RunID, AttemptID, JobID string
	StartBoundary                                      string
}

// BindRequest records what a launch receipt reported: the session and, when
// the provider says, the model it actually used.
type BindRequest struct{ SourceID, SessionID, Model string }

// Source is one registered source. Model is the requested model and
// ActualModel the one the provider reported.
type Source struct {
	ID, CollaborationID, Provider, Model, ActualModel, Role string
	SessionID, ParentSourceID, RunID, AttemptID, JobID      string
	StartBoundary, Status                                   string
}

// ModelMismatch reports a provider that ran another model than requested.
func (s Source) ModelMismatch() bool {
	return s.Model != "" && s.ActualModel != "" && s.Model != s.ActualModel
}

const sourceColumns = `id,collaboration_id,provider,COALESCE(model,''),COALESCE(actual_model,''),role,COALESCE(session_id,''),
	COALESCE(parent_source_id,''),COALESCE(run_id,''),COALESCE(attempt_id,''),COALESCE(job_id,''),COALESCE(start_boundary,''),status`

type rowScanner interface{ Scan(...any) error }

func scanSource(r rowScanner) (Source, error) {
	var s Source
	err := r.Scan(&s.ID, &s.CollaborationID, &s.Provider, &s.Model, &s.ActualModel, &s.Role, &s.SessionID,
		&s.ParentSourceID, &s.RunID, &s.AttemptID, &s.JobID, &s.StartBoundary, &s.Status)
	return s, err
}

func sourceTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Source, error) {
	s, err := scanSource(q.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM expected_sources WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("%w: %s", ErrNoSource, id)
	}
	return s, err
}

// Source returns a registered source.
func (s *Store) Source(ctx context.Context, id string) (Source, error) {
	return sourceTx(ctx, s.db, id)
}

// RegisterSource records an expected source before its launch is sent. It
// needs an open collaboration: a closing one admits no new launch. Repeating
// an identical request succeeds, so a launch retried after a crash reuses its
// registration; any other request under the same id is ErrConflict.
func (s *Store) RegisterSource(ctx context.Context, req SourceRequest) error {
	return s.registerSource(ctx, req, true)
}

// RegisterDiscoveredChild records a session that collection found descended
// from a registered source: a subagent the agent launched itself. It is not a
// launch, so it is recorded even after the collaboration stopped admitting
// launches, and it always belongs to its parent's collaboration.
func (s *Store) RegisterDiscoveredChild(ctx context.Context, req SourceRequest) error {
	if req.ParentSourceID == "" || req.SessionID == "" {
		return invalid("a discovered child names its parent source and its session")
	}
	return s.registerSource(ctx, req, false)
}

func (s *Store) registerSource(ctx context.Context, req SourceRequest, launch bool) error {
	if err := s.writable(); err != nil {
		return err
	}
	switch {
	case !idPattern.MatchString(req.ID) || req.Provider == "" || req.Role == "":
		return invalid("a source needs a 32 hex character id, a provider and a role")
	case req.AttemptID != "" && req.RunID == "":
		return invalid("an attempt is named with its run")
	case req.Role == "planner" && req.SessionID == "":
		return invalid("a planner source names its session, so its messages can be bounded")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if req.ParentSourceID != "" {
		parent, err := sourceTx(ctx, tx.conn, req.ParentSourceID)
		if err != nil {
			return err
		}
		if req.CollaborationID != "" && req.CollaborationID != parent.CollaborationID {
			return invalid("source %s is a child of %s and belongs to its collaboration %s, not %s",
				req.ID, parent.ID, parent.CollaborationID, req.CollaborationID)
		}
		req.CollaborationID = parent.CollaborationID
	}
	if req.CollaborationID == "" {
		return invalid("a source names its collaboration or its parent source")
	}
	if existing, err := sourceTx(ctx, tx.conn, req.ID); err == nil {
		if !sameRegistration(existing, req) {
			return fmt.Errorf("%w: source %s is registered differently", workflow.ErrConflict, req.ID)
		}
		return nil
	} else if !errors.Is(err, ErrNoSource) {
		return err
	}
	if launch {
		if err := requireLaunchable(ctx, tx, req.CollaborationID); err != nil {
			return err
		}
	}
	status := "expected"
	if req.SessionID != "" {
		status = "bound"
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO expected_sources(id,collaboration_id,provider,model,role,session_id,
		parent_source_id,run_id,attempt_id,job_id,start_boundary,status,created_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.ID, req.CollaborationID, req.Provider, nullable(req.Model), req.Role, nullable(req.SessionID),
		nullable(req.ParentSourceID), nullable(req.RunID), nullable(req.AttemptID), nullable(req.JobID),
		nullable(req.StartBoundary), status, s.now().UnixMilli()); err != nil {
		if isForeignKey(err) {
			return invalid("source %s names a run, attempt or job that does not exist", req.ID)
		}
		return err
	}
	if req.Role == "planner" {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO session_segments(id,collaboration_id,source_id,provider,session_id,role,start_after,opened_ms)
			VALUES(?,?,?,?,?,'planner',?,?)`, req.ID, req.CollaborationID, req.ID, req.Provider, req.SessionID,
			nullable(req.StartBoundary), s.now().UnixMilli()); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return fmt.Errorf("%w: planner session %s is still owned by an open segment; close it first", workflow.ErrConflict, req.SessionID)
			}
			return err
		}
	}
	return tx.commit(ctx)
}

func sameRegistration(s Source, r SourceRequest) bool {
	return s.CollaborationID == r.CollaborationID && s.Provider == r.Provider && s.Model == r.Model && s.Role == r.Role &&
		s.ParentSourceID == r.ParentSourceID && s.RunID == r.RunID && s.AttemptID == r.AttemptID && s.JobID == r.JobID &&
		s.StartBoundary == r.StartBoundary && (r.SessionID == "" || s.SessionID == r.SessionID)
}

// BindSourceSession records a launch receipt's session and reported model.
// The owner never changes, and a child launched before its collaboration
// stopped taking launches still binds afterwards. The same receipt again is a
// no-op; a different session for the same launch is ErrConflict.
func (s *Store) BindSourceSession(ctx context.Context, req BindRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if req.SessionID == "" {
		return invalid("an unknown receipt binds nothing; the source stays unresolved")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	src, err := sourceTx(ctx, tx.conn, req.SourceID)
	if err != nil {
		return err
	}
	if src.SessionID != "" && src.SessionID != req.SessionID {
		return fmt.Errorf("%w: source %s is bound to session %s, not %s", workflow.ErrConflict, src.ID, src.SessionID, req.SessionID)
	}
	if src.ActualModel != "" && req.Model != "" && src.ActualModel != req.Model {
		return fmt.Errorf("%w: source %s reported model %s before, not %s", workflow.ErrConflict, src.ID, src.ActualModel, req.Model)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE expected_sources SET session_id=?, actual_model=COALESCE(actual_model,?),
		status=CASE WHEN status='expected' THEN 'bound' ELSE status END WHERE id=?`, req.SessionID, nullable(req.Model), src.ID); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// CloseSegment ends a planner's ownership of its session at the message
// endThrough. It is refused while a child the planner registered has not been
// launched, so a launch cannot outlive the record of who owns it. Closing
// again at the same boundary is a no-op.
func (s *Store) CloseSegment(ctx context.Context, sourceID, endThrough string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if endThrough == "" {
		return invalid("a segment closes at a named message")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var end sql.NullString
	var closed sql.NullInt64
	err = tx.conn.QueryRowContext(ctx, `SELECT end_through, closed_at FROM session_segments WHERE source_id=? ORDER BY opened_ms DESC LIMIT 1`,
		sourceID).Scan(&end, &closed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: source %s has no session segment", ErrNoSource, sourceID)
	case err != nil:
		return err
	case closed.Valid && end.String == endThrough:
		return nil
	case closed.Valid:
		return fmt.Errorf("%w: the segment of %s closed at %s, not %s", workflow.ErrConflict, sourceID, end.String, endThrough)
	}
	var pending int
	if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM expected_sources WHERE parent_source_id=? AND status='expected'`,
		sourceID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return invalid("%d child launch(es) of %s are registered but not yet launched", pending, sourceID)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE session_segments SET end_through=?, closed_at=? WHERE source_id=? AND closed_at IS NULL`,
		endThrough, s.now().UnixMilli(), sourceID); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// UnresolvedSources lists a collaboration's sources that no receipt has bound
// yet. After a restart these are reconciled, never launched again.
func (s *Store) UnresolvedSources(ctx context.Context, collaborationID string) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM expected_sources
		WHERE collaboration_id=? AND status='expected' ORDER BY created_ms, id`, collaborationID)
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
