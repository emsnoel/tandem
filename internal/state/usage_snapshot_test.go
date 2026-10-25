package state

import (
	"errors"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func TestUsageCommitIsAtomicAndCompareAndSwap(t *testing.T) {
	e := newCollabEnv(t)
	first := UsageCommit{Provider: "claude", SessionID: "s1", Cursor: []byte(`{"offset":100}`), Offset: 100, PrefixHash: "h1",
		Events: []StoredEvent{{EventID: "m1", Revision: 0, FirstCursor: 0, JSON: []byte(`{"output":1}`)}}}
	if err := e.s.CommitUsage(ctx, first); err != nil {
		t.Fatal(err)
	}
	// A replay of the committed step is refused and changes nothing.
	if err := e.s.CommitUsage(ctx, first); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a replayed step: %v", err)
	}
	second := UsageCommit{Provider: "claude", SessionID: "s1", PrevOffset: 100, Cursor: []byte(`{"offset":180}`), Offset: 180, PrefixHash: "h2",
		Events: []StoredEvent{{EventID: "m1", Revision: 1, FirstCursor: 3, JSON: []byte(`{"output":9}`)},
			{EventID: "m2", Revision: 0, FirstCursor: 4, JSON: []byte(`{"output":2}`)}}}
	if err := e.s.CommitUsage(ctx, second); err != nil {
		t.Fatal(err)
	}
	events, err := e.s.UsageEvents(ctx, "claude", "s1")
	if err != nil || len(events) != 2 {
		t.Fatalf("events %+v %v", events, err)
	}
	if events[0].EventID != "m1" || events[0].Revision != 1 || string(events[0].JSON) != `{"output":9}` || events[0].FirstCursor != 0 {
		t.Fatalf("the revision did not replace the contribution, or moved its first position: %+v", events[0])
	}
	var revisions int
	e.s.db.QueryRow(`SELECT count(*) FROM usage_revisions WHERE session_id='s1'`).Scan(&revisions)
	if revisions != 3 {
		t.Fatalf("%d revisions kept, want every one", revisions)
	}
	cursor, offset, found, err := e.s.UsageCursor(ctx, "claude", "s1")
	if err != nil || !found || offset != 180 || string(cursor) != `{"offset":180}` {
		t.Fatalf("cursor %s %d %v %v", cursor, offset, found, err)
	}
	// An older revision arriving late never replaces a newer one.
	late := UsageCommit{Provider: "claude", SessionID: "s1", PrevOffset: 180, Cursor: []byte(`{}`), Offset: 180, PrefixHash: "h2",
		Events: []StoredEvent{{EventID: "m1", Revision: 0, JSON: []byte(`{"output":1}`)}}}
	if err := e.s.CommitUsage(ctx, late); err != nil {
		t.Fatal(err)
	}
	if events, _ := e.s.UsageEvents(ctx, "claude", "s1"); events[0].Revision != 1 {
		t.Fatalf("an older revision replaced a newer one: %+v", events[0])
	}
	if err := e.s.CommitUsage(ctx, UsageCommit{Provider: "claude", SessionID: "s1", PrevOffset: 180, Offset: 10}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a cursor moved backwards: %v", err)
	}
}

func TestSnapshotRevisionsSupersedeAndInvalidate(t *testing.T) {
	e := newCollabEnv(t)
	doc1, doc2 := e.a.artifact(t, `{"snapshot":1}`), e.a.artifact(t, `{"snapshot":2}`)
	first, created, err := e.s.PutUsageSnapshot(ctx, e.collabID, doc1, true)
	if err != nil || !created || first.Revision != 1 || first.Supersedes != "" {
		t.Fatalf("first %+v %v %v", first, created, err)
	}
	if again, created, _ := e.s.PutUsageSnapshot(ctx, e.collabID, doc1, true); created || again.ID != first.ID {
		t.Fatalf("an identical document made a new revision: %+v", again)
	}
	second, created, err := e.s.PutUsageSnapshot(ctx, e.collabID, doc2, false)
	if err != nil || !created || second.Revision != 2 || second.Supersedes != first.ID {
		t.Fatalf("second %+v %v %v", second, created, err)
	}
	old, err := e.s.UsageSnapshot(ctx, first.ID)
	if err != nil || old.SupersededBy != second.ID {
		t.Fatalf("the superseded snapshot was not invalidated: %+v %v", old, err)
	}
	latest, found, _ := e.s.LatestUsageSnapshot(ctx, e.collabID)
	if !found || latest.ID != second.ID || latest.SupersededBy != "" {
		t.Fatalf("latest %+v", latest)
	}
	var refs int
	e.s.db.QueryRow(`SELECT count(*) FROM artifact_refs WHERE owner_kind='usage_snapshot'`).Scan(&refs)
	if refs != 2 {
		t.Fatalf("%d snapshot artifact refs", refs)
	}
}

func TestCollaborationCloseFreezesLaunchesButNotCollection(t *testing.T) {
	e := newSourceEnv(t)
	e.register(t, e.planner(e.collabID, 1001, ""))
	if err := e.s.BeginClose(ctx, e.collabID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.BeginClose(ctx, e.collabID); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
	err := e.s.RegisterSource(ctx, SourceRequest{ID: hexID(1002), ParentSourceID: hexID(1001), Provider: "codex", Role: "closeout"})
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("a launch registered after the freeze: %v", err)
	}
	// A subagent found by collection is accounted, launch or not.
	if err := e.s.RegisterDiscoveredChild(ctx, SourceRequest{ID: hexID(1003), ParentSourceID: hexID(1001), Provider: "claude",
		Role: "subagent", SessionID: "sub-1"}); err != nil {
		t.Fatalf("a discovered child after the freeze: %v", err)
	}
	if child, _ := e.s.Source(ctx, hexID(1003)); child.CollaborationID != e.collabID || child.Status != "bound" {
		t.Fatalf("child %+v", child)
	}
	if err := e.s.MarkSourceTerminal(ctx, hexID(1003)); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MarkClosed(ctx, e.collabID); err != nil {
		t.Fatal(err)
	}
	if status, _ := e.s.CollaborationStatus(ctx, e.collabID); status != "closed" {
		t.Fatalf("status %s", status)
	}
	if err := e.s.MarkClosed(ctx, e.collabB); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("closing an open collaboration without the freeze: %v", err)
	}
	segments, err := e.s.SessionSegments(ctx, "claude", "planner-session")
	if err != nil || len(segments) != 1 || segments[0].CollaborationID != e.collabID || segments[0].Closed {
		t.Fatalf("segments %+v %v", segments, err)
	}
}
