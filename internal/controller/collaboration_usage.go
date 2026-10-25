package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/usageledger"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Collaboration usage: incremental collection into immutable snapshots.
//
// Collection runs after every tick of a run in a collaboration, after a stop,
// and on archive. Each session is read from its committed cursor, and events
// and cursor are committed together. A changed snapshot becomes the next
// revision. Admissions whose sources are final are settled from actual usage;
// one with unknown usage keeps its reservation.

// SessionLogs finds a provider's own log of a session. Reading only.
type SessionLogs interface {
	// Path is the session's log, or "" when none is found. A provider with no
	// readable logs is workflow.ErrUnsupported.
	Path(ctx context.Context, provider, session string) (string, error)
	// Children are the sessions the session started, at any depth, parents
	// before children, filed between from and to.
	Children(ctx context.Context, provider, session string, from, to time.Time) ([]rollout.Found, error)
}

// ProviderLogs reads Codex rollout logs and Claude Code transcripts.
type ProviderLogs struct{ CodexSessions, ClaudeProjects string }

// DefaultProviderLogs is where the two CLIs file their logs by default.
func DefaultProviderLogs() ProviderLogs {
	home, _ := os.UserHomeDir()
	claude := filepath.Join(home, ".claude", "projects")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claude = filepath.Join(dir, "projects")
	}
	return ProviderLogs{CodexSessions: DefaultCodexSessions(), ClaudeProjects: claude}
}

func (p ProviderLogs) Path(_ context.Context, provider, session string) (string, error) {
	switch provider {
	case "codex":
		return rollout.FindSession(p.CodexSessions, session)
	case "claude":
		m, err := filepath.Glob(filepath.Join(p.ClaudeProjects, "*", filepath.Base(session)+".jsonl"))
		if err != nil || len(m) == 0 || filepath.Base(session) != session {
			return "", err
		}
		return m[0], nil
	}
	return "", fmt.Errorf("%w: no readable logs for %s", workflow.ErrUnsupported, provider)
}

// Children finds Codex sessions forked from session. Claude subagent
// transcripts are not discovered: their format is not verified, so a Claude
// session's children are left out rather than guessed.
func (p ProviderLogs) Children(_ context.Context, provider, session string, from, to time.Time) ([]rollout.Found, error) {
	if provider != "codex" {
		return nil, nil
	}
	return rollout.Descendants(p.CodexSessions, session, from, to)
}

// CollectCollaboration reads what is new in every source of the collaboration
// and stores a snapshot revision if anything changed.
func (c *Controller) CollectCollaboration(ctx context.Context, id string) error {
	_, err := c.collectCollaboration(ctx, id)
	return err
}

// CollaborationReport is a collaboration's current snapshot.
type CollaborationReport struct {
	Record   state.SnapshotRecord `json:"record"`
	Snapshot usageledger.Snapshot `json:"snapshot"`
}

// ReportCollaboration reads the stored current snapshot without collecting.
func (c *Controller) ReportCollaboration(ctx context.Context, id string) (CollaborationReport, error) {
	rec, found, err := c.d.State.LatestUsageSnapshot(ctx, id)
	if err != nil || !found {
		return CollaborationReport{}, errors.Join(err, fmt.Errorf("%w: collaboration %s has no snapshot yet", state.ErrNoSource, id))
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, rec.ContentHash, 64<<20)
	if err != nil {
		return CollaborationReport{}, err
	}
	rep := CollaborationReport{Record: rec}
	return rep, json.Unmarshal(raw, &rep.Snapshot)
}

// CloseCollaboration freezes the collaboration's launches, then collects. If
// the snapshot is complete but for the freeze itself, the collaboration is
// closed and a final snapshot stored; otherwise it stays closing and its
// snapshot says what is missing. Nothing paid starts after the freeze.
func (c *Controller) CloseCollaboration(ctx context.Context, id string) error {
	if err := c.d.State.BeginClose(ctx, id); err != nil {
		return err
	}
	snap, err := c.collectCollaboration(ctx, id)
	if err != nil {
		return err
	}
	if len(snap.Reasons) != 1 || snap.Reasons[0] != "collaboration_closing:"+id {
		return nil
	}
	if err := c.d.State.MarkClosed(ctx, id); err != nil {
		return err
	}
	_, err = c.collectCollaboration(ctx, id)
	return err
}

// CollectRun collects the collaboration a run belongs to, if any: on archive,
// for one, which leaves the collaboration's snapshot provisional.
func (c *Controller) CollectRun(ctx context.Context, runID string) error {
	return c.collectAfter(ctx, runID)
}

// collectAfter collects a run's collaboration, if it has one.
func (c *Controller) collectAfter(ctx context.Context, runID string) error {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	cfg, err := c.config(ctx, run)
	if err != nil || cfg.Collaboration == "" {
		return err
	}
	return c.CollectCollaboration(ctx, cfg.Collaboration)
}

func (c *Controller) collectCollaboration(ctx context.Context, id string) (usageledger.Snapshot, error) {
	status, err := c.d.State.CollaborationStatus(ctx, id)
	if err != nil {
		return usageledger.Snapshot{}, err
	}
	if err := c.discoverChildren(ctx, id); err != nil {
		return usageledger.Snapshot{}, err
	}
	sources, err := c.d.State.ListSources(ctx, id)
	if err != nil {
		return usageledger.Snapshot{}, err
	}
	in := usageledger.SnapshotInput{CollaborationID: id, Status: status}
	read := map[string]bool{}
	settled := map[string]bool{}
	for _, src := range sources {
		view, err := c.snapshotSource(ctx, src, settled)
		if err != nil {
			return usageledger.Snapshot{}, err
		}
		in.Sources = append(in.Sources, view)
		key := src.Provider + ":" + src.SessionID
		if src.SessionID == "" || read[key] {
			continue
		}
		read[key] = true
		log, err := c.collectSession(ctx, src.Provider, src.SessionID)
		if err != nil {
			return usageledger.Snapshot{}, err
		}
		in.Logs = append(in.Logs, log)
	}
	snap := usageledger.BuildSnapshot(in)
	hash, err := c.putJSON(ctx, snap)
	if err != nil {
		return snap, err
	}
	if _, _, err := c.d.State.PutUsageSnapshot(ctx, id, hash, snap.Complete); err != nil {
		return snap, err
	}
	return snap, c.settleAdmissions(ctx, id, snap)
}
