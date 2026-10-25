package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/usageledger"
	"github.com/emsnoel/tandem/internal/workflow"
)

// discoverChildren registers the sessions the collaboration's sources started
// themselves (Codex subagents), each under its parent and owned as it is.
func (c *Controller) discoverChildren(ctx context.Context, id string) error {
	if c.d.SessionLogs == nil {
		return nil
	}
	created, err := c.d.State.CollaborationCreatedMS(ctx, id)
	if err != nil {
		return err
	}
	sources, err := c.d.State.ListSources(ctx, id)
	if err != nil {
		return err
	}
	bySession := map[string]string{}
	for _, s := range sources {
		if s.SessionID != "" {
			bySession[s.Provider+":"+s.SessionID] = s.ID
		}
	}
	from, to := time.UnixMilli(created), c.d.Now()
	for _, s := range sources {
		if s.SessionID == "" {
			continue
		}
		found, err := c.d.SessionLogs.Children(ctx, s.Provider, s.SessionID, from, to)
		if err != nil {
			return err
		}
		for _, f := range found {
			key := s.Provider + ":" + f.SessionID
			parent, known := bySession[s.Provider+":"+f.ParentID]
			if _, registered := bySession[key]; registered || !known {
				continue
			}
			childID := derive("discovered", parent, f.SessionID)
			if err := c.d.State.RegisterDiscoveredChild(ctx, state.SourceRequest{ID: childID, ParentSourceID: parent,
				Provider: s.Provider, Role: "subagent", SessionID: f.SessionID, StartBoundary: "discovered"}); err != nil {
				return err
			}
			bySession[key] = childID
		}
	}
	return nil
}

// snapshotSource decides whether a source's work has ended: marked terminal,
// its job settled, its attempt's jobs of its role all settled, or, for a
// discovered subagent, its parent settled. A planner's segment is attached.
func (c *Controller) snapshotSource(ctx context.Context, src state.Source, settled map[string]bool) (usageledger.SnapshotSource, error) {
	view := usageledger.SnapshotSource{ID: src.ID, Provider: src.Provider, Role: src.Role, SessionID: src.SessionID}
	var done bool
	var err error
	switch {
	case src.Status == "terminal":
		done = true
	case src.JobID != "":
		done, err = c.d.State.JobSettled(ctx, src.JobID)
	case src.AttemptID != "" && (src.Role == "builder" || src.Role == "reviewer"):
		done, err = c.d.State.AttemptJobsSettled(ctx, src.AttemptID, src.Role)
	case src.Role == "subagent" && src.ParentSourceID != "":
		done = settled[src.ParentSourceID]
	}
	if err != nil {
		return view, err
	}
	settled[src.ID], view.Settled = done, done
	if src.Role == "planner" && src.SessionID != "" {
		segments, err := c.d.State.SessionSegments(ctx, src.Provider, src.SessionID)
		if err != nil {
			return view, err
		}
		for _, g := range segments {
			if g.SourceID == src.ID {
				view.Segment = &usageledger.SnapshotSegment{CollaborationID: g.CollaborationID, SourceID: g.SourceID,
					StartAfter: g.StartAfter, EndThrough: g.EndThrough, Closed: g.Closed}
			}
		}
	}
	return view, nil
}

// collectSession reads a provider session from its committed cursor on,
// commits what it found, and returns the session as stored.
func (c *Controller) collectSession(ctx context.Context, provider, session string) (usageledger.SessionLog, error) {
	log := usageledger.SessionLog{Provider: provider, SessionID: session}
	segments, err := c.d.State.SessionSegments(ctx, provider, session)
	if err != nil {
		return log, err
	}
	for _, g := range segments {
		log.Segments = append(log.Segments, usageledger.SnapshotSegment{CollaborationID: g.CollaborationID, SourceID: g.SourceID,
			StartAfter: g.StartAfter, EndThrough: g.EndThrough, Closed: g.Closed})
	}
	stored, offset, found, err := c.d.State.UsageCursor(ctx, provider, session)
	if err != nil {
		return log, err
	}
	var cur usageledger.Cursor
	if found {
		if err := json.Unmarshal(stored, &cur); err != nil {
			return log, fmt.Errorf("the usage cursor of %s %s is corrupt: %w", provider, session, err)
		}
	}
	data, why, err := c.readFrom(ctx, provider, session, offset)
	if err != nil {
		return log, err
	}
	switch {
	case why != "" && !found:
		log.Unavailable = why
		return log, nil
	case why != "":
		log.Problem = why // read before: keep what was stored
	default:
		batch, next, err := usageledger.NormalizeIncremental(provider, data, cur)
		if err != nil {
			return log, err
		}
		if next.Offset > cur.Offset || len(batch.Events) > 0 || !found {
			if err := c.commitSession(ctx, provider, session, offset, batch, next); err != nil {
				return log, err
			}
		}
		log.Held = data[next.Offset-cur.Offset:]
		cur = next
	}
	rows, err := c.d.State.UsageEvents(ctx, provider, session)
	if err != nil {
		return log, err
	}
	for _, r := range rows {
		var e usageledger.Event
		if err := json.Unmarshal(r.JSON, &e); err != nil {
			return log, fmt.Errorf("stored usage event %s is corrupt: %w", r.EventID, err)
		}
		log.Events = append(log.Events, e)
	}
	log.Cursor = cur
	return log, nil
}

// readFrom reads a session's log from offset on. why names a log that cannot
// be read, or that is now shorter than what was already read.
func (c *Controller) readFrom(ctx context.Context, provider, session string, offset int64) (data []byte, why string, err error) {
	if c.d.SessionLogs == nil {
		return nil, "no session log reader is configured", nil
	}
	path, err := c.d.SessionLogs.Path(ctx, provider, session)
	switch {
	case errors.Is(err, workflow.ErrUnsupported):
		return nil, err.Error(), nil
	case err != nil:
		return nil, "", err
	case path == "":
		return nil, "source_disappeared", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "source_disappeared", nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if info.Size() < offset {
		return nil, "source_truncated", nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, "", err
	}
	data, err = io.ReadAll(f)
	return data, "", err
}

func (c *Controller) commitSession(ctx context.Context, provider, session string, prev int64, batch usageledger.Batch, next usageledger.Cursor) error {
	cursor, err := json.Marshal(next)
	if err != nil {
		return err
	}
	commit := state.UsageCommit{Provider: provider, SessionID: session, PrevOffset: prev, Cursor: cursor, Offset: next.Offset, PrefixHash: next.PrefixHash}
	for _, e := range batch.Events {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		commit.Events = append(commit.Events, state.StoredEvent{EventID: e.EventID, Revision: e.Revision, FirstCursor: e.SourceCursor, JSON: raw})
	}
	return c.d.State.CommitUsage(ctx, commit)
}

// settleAdmissions settles the admissions whose sources are final. A session's
// usage, less what its earlier admissions already settled, goes to the first
// of them and nothing to the rest, so a shared session is counted once.
func (c *Controller) settleAdmissions(ctx context.Context, id string, snap usageledger.Snapshot) error {
	flight, err := c.d.State.InFlightAdmissions(ctx, id)
	if err != nil || len(flight) == 0 {
		return err
	}
	final := map[string]usageledger.SourceStatus{}
	for _, s := range snap.ExpectedSources {
		final[s.ID] = s
	}
	given := map[string]bool{}
	for _, a := range flight {
		src, ok := final[a.SourceID]
		if !ok || !src.Final {
			continue
		}
		var in, out *int64
		if !given[src.SourceKey] {
			given[src.SourceKey] = true
			in, out = sessionUsage(snap, src.SourceKey)
			if in != nil && out != nil {
				doneIn, doneOut, err := c.d.State.SettledSessionUsage(ctx, id, src.Provider, src.SessionID)
				if err != nil {
					return err
				}
				in, out = nonNegative(*in-doneIn), nonNegative(*out-doneOut)
			}
		} else {
			zero := int64(0)
			in, out = &zero, &zero
		}
		if err := c.d.State.SettleAdmission(ctx, state.SettleRequest{AdmissionID: a.ID, InputUsed: in, OutputUsed: out}); err != nil {
			return err
		}
	}
	return nil
}

// sessionUsage sums a session's owned input and output; nil when unknown.
func sessionUsage(snap usageledger.Snapshot, key string) (*int64, *int64) {
	var in, out int64
	for _, e := range snap.Events {
		if e.SourceKey != key {
			continue
		}
		if e.InputTotal == nil || e.Output == nil {
			return nil, nil
		}
		in, out = in+*e.InputTotal, out+*e.Output
	}
	return &in, &out
}

func nonNegative(v int64) *int64 {
	if v < 0 {
		v = 0
	}
	return &v
}
