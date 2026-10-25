package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Collaboration usage: incremental collection state, immutable snapshots and
// the close lifecycle. The parser and the snapshot's content belong to
// internal/usageledger; this file stores what they produce, atomically.

// StoredEvent is one event's latest revision as the ledger keeps it.
type StoredEvent struct {
	EventID     string
	Revision    int64
	FirstCursor int
	JSON        []byte
}

// UsageCommit is one collection step for a provider session: the events it
// found or revised and the cursor to continue from. PrevOffset is the offset
// of the cursor it continued, 0 for the first step.
type UsageCommit struct {
	Provider, SessionID string
	PrevOffset          int64
	Cursor              []byte
	Offset              int64
	PrefixHash          string
	Events              []StoredEvent
}

// SnapshotRecord is one stored snapshot revision.
type SnapshotRecord struct {
	ID              string `json:"snapshot_id"`
	CollaborationID string `json:"collaboration_id"`
	ContentHash     string `json:"content_hash"`
	Supersedes      string `json:"supersedes,omitempty"`
	SupersededBy    string `json:"superseded_by,omitempty"`
	Revision        int64  `json:"revision"`
	Complete        bool   `json:"complete"`
}

// UsageCursor returns a provider session's committed cursor, if any.
func (s *Store) UsageCursor(ctx context.Context, provider, sessionID string) (cursor []byte, offset int64, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT cursor_json, offset_bytes FROM usage_cursors WHERE provider=? AND session_id=?`,
		provider, sessionID).Scan(&cursor, &offset)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	return cursor, offset, err == nil, err
}

// CommitUsage stores a collection step: each event revision, the latest per
// event, and the cursor, in one transaction. It is refused with ErrConflict
// unless the stored cursor is still at PrevOffset, so two collectors cannot
// both advance a session, and a replay of a committed step changes nothing.
func (s *Store) CommitUsage(ctx context.Context, c UsageCommit) error {
	if err := s.writable(); err != nil {
		return err
	}
	if c.Provider == "" || c.SessionID == "" || c.Offset < c.PrevOffset {
		return invalid("a usage commit names its provider session and does not move its cursor backwards")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	var stored int64
	switch err := tx.conn.QueryRowContext(ctx, `SELECT offset_bytes FROM usage_cursors WHERE provider=? AND session_id=?`,
		c.Provider, c.SessionID).Scan(&stored); {
	case errors.Is(err, sql.ErrNoRows):
		stored = 0
	case err != nil:
		return err
	}
	if stored != c.PrevOffset {
		return fmt.Errorf("%w: the cursor of %s %s is at %d, not %d", workflow.ErrConflict, c.Provider, c.SessionID, stored, c.PrevOffset)
	}
	now := s.now().UnixMilli()
	for _, e := range c.Events {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO usage_revisions(provider,session_id,event_id,revision,event_json,created_ms)
			VALUES(?,?,?,?,?,?)`, c.Provider, c.SessionID, e.EventID, e.Revision, string(e.JSON), now); err != nil {
			return err
		}
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO usage_events(provider,session_id,event_id,revision,first_cursor,event_json)
			VALUES(?,?,?,?,?,?) ON CONFLICT(provider,session_id,event_id) DO UPDATE SET revision=excluded.revision, event_json=excluded.event_json
			WHERE excluded.revision>usage_events.revision`, c.Provider, c.SessionID, e.EventID, e.Revision, e.FirstCursor, string(e.JSON)); err != nil {
			return err
		}
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO usage_cursors(provider,session_id,cursor_json,offset_bytes,prefix_hash,updated_ms)
		VALUES(?,?,?,?,?,?) ON CONFLICT(provider,session_id) DO UPDATE SET cursor_json=excluded.cursor_json,
		offset_bytes=excluded.offset_bytes, prefix_hash=excluded.prefix_hash, updated_ms=excluded.updated_ms`,
		c.Provider, c.SessionID, string(c.Cursor), c.Offset, c.PrefixHash, now); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// UsageEvents returns a provider session's latest event revisions in the
// order they first appeared.
func (s *Store) UsageEvents(ctx context.Context, provider, sessionID string) ([]StoredEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,revision,first_cursor,event_json FROM usage_events
		WHERE provider=? AND session_id=? ORDER BY first_cursor, event_id`, provider, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredEvent
	for rows.Next() {
		var e StoredEvent
		if err := rows.Scan(&e.EventID, &e.Revision, &e.FirstCursor, &e.JSON); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutUsageSnapshot stores a snapshot document (already an artifact) as the
// collaboration's next revision, superseding and invalidating the current one.
// A document identical to the current one is not stored again.
func (s *Store) PutUsageSnapshot(ctx context.Context, collaborationID, contentHash string, complete bool) (SnapshotRecord, bool, error) {
	if err := s.writable(); err != nil {
		return SnapshotRecord{}, false, err
	}
	if !hashPattern.MatchString(contentHash) {
		return SnapshotRecord{}, false, invalid("a snapshot is an artifact hash")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return SnapshotRecord{}, false, err
	}
	defer tx.rollback()
	latest, found, err := latestSnapshotTx(ctx, tx.conn, collaborationID)
	if err != nil {
		return SnapshotRecord{}, false, err
	}
	if found && latest.ContentHash == contentHash {
		return latest, false, nil
	}
	rec := SnapshotRecord{CollaborationID: collaborationID, ContentHash: contentHash, Complete: complete, Revision: 1}
	if found {
		rec.Revision, rec.Supersedes = latest.Revision+1, latest.ID
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", collaborationID, rec.Revision, contentHash, rec.Supersedes)))
	rec.ID = hex.EncodeToString(sum[:])
	now := s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO usage_snapshots(id,collaboration_id,revision,supersedes_id,content_hash,complete,created_ms)
		VALUES(?,?,?,?,?,?,?)`, rec.ID, collaborationID, rec.Revision, nullable(rec.Supersedes), contentHash, complete, now); err != nil {
		if isForeignKey(err) {
			return SnapshotRecord{}, false, invalid("snapshot %s or collaboration %s does not exist", contentHash, collaborationID)
		}
		return SnapshotRecord{}, false, err
	}
	if found {
		if _, err := tx.conn.ExecContext(ctx, `UPDATE usage_snapshots SET superseded_by=?, invalidated_ms=? WHERE id=?`, rec.ID, now, latest.ID); err != nil {
			return SnapshotRecord{}, false, err
		}
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('usage_snapshot',?,'document',?)`,
		rec.ID, contentHash); err != nil {
		return SnapshotRecord{}, false, err
	}
	return rec, true, tx.commit(ctx)
}

const snapshotColumns = `id,collaboration_id,content_hash,COALESCE(supersedes_id,''),COALESCE(superseded_by,''),revision,complete`

func scanSnapshot(r rowScanner) (SnapshotRecord, error) {
	var rec SnapshotRecord
	err := r.Scan(&rec.ID, &rec.CollaborationID, &rec.ContentHash, &rec.Supersedes, &rec.SupersededBy, &rec.Revision, &rec.Complete)
	return rec, err
}

func latestSnapshotTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, collaborationID string) (SnapshotRecord, bool, error) {
	rec, err := scanSnapshot(q.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM usage_snapshots
		WHERE collaboration_id=? ORDER BY revision DESC LIMIT 1`, collaborationID))
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotRecord{}, false, nil
	}
	return rec, err == nil, err
}

// LatestUsageSnapshot returns the collaboration's current snapshot revision.
func (s *Store) LatestUsageSnapshot(ctx context.Context, collaborationID string) (SnapshotRecord, bool, error) {
	return latestSnapshotTx(ctx, s.db, collaborationID)
}

// UsageSnapshot returns a stored revision; SupersededBy says whether it is
// still current.
func (s *Store) UsageSnapshot(ctx context.Context, id string) (SnapshotRecord, error) {
	rec, err := scanSnapshot(s.db.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM usage_snapshots WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return rec, fmt.Errorf("%w: no snapshot %s", ErrNoSource, id)
	}
	return rec, err
}
