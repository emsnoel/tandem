package state

import (
	"errors"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// sourceEnv is a store with two open collaborations, A and B.
type sourceEnv struct {
	*collabEnv
	collabB string
}

func newSourceEnv(t *testing.T) *sourceEnv {
	t.Helper()
	e := &sourceEnv{collabEnv: newCollabEnv(t), collabB: hexID(910)}
	if err := e.s.CreateCollaboration(ctx, CollaborationRequest{ID: e.collabB, PolicyHash: e.policyHash}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *sourceEnv) planner(collab string, id int, after string) SourceRequest {
	return SourceRequest{ID: hexID(id), CollaborationID: collab, Provider: "claude", Model: "claude-a", Role: "planner",
		SessionID: "planner-session", StartBoundary: after}
}

func (e *sourceEnv) register(t *testing.T, req SourceRequest) {
	t.Helper()
	if err := e.s.RegisterSource(ctx, req); err != nil {
		t.Fatal(err)
	}
}

func TestSourceOwnershipExclusive(t *testing.T) {
	e := newSourceEnv(t)
	e.register(t, e.planner(e.collabID, 1001, "m1"))
	err := e.s.RegisterSource(ctx, e.planner(e.collabB, 1002, "m5"))
	if !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a second collaboration took a planner session that is still owned: %v", err)
	}
	if _, err := e.s.Source(ctx, hexID(1002)); !errors.Is(err, ErrNoSource) {
		t.Fatalf("the refused registration left a row: %v", err)
	}
	if err := e.s.CloseSegment(ctx, hexID(1001), "m4"); err != nil {
		t.Fatal(err)
	}
	e.register(t, e.planner(e.collabB, 1002, "m4"))
	var open int
	if err := e.s.db.QueryRow(`SELECT count(*) FROM session_segments WHERE session_id='planner-session' AND closed_at IS NULL`).Scan(&open); err != nil || open != 1 {
		t.Fatalf("%d open planner segments, %v", open, err)
	}
	var start, end string
	if err := e.s.db.QueryRow(`SELECT start_after, end_through FROM session_segments WHERE source_id=?`, hexID(1001)).Scan(&start, &end); err != nil ||
		start != "m1" || end != "m4" {
		t.Fatalf("segment boundaries %q..%q, %v", start, end, err)
	}
	if err := e.s.CloseSegment(ctx, hexID(1001), "m4"); err != nil {
		t.Fatalf("closing an already closed segment at the same boundary: %v", err)
	}
	if err := e.s.CloseSegment(ctx, hexID(1001), "m9"); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("re-closing at another boundary: %v", err)
	}
}

func TestSourcePlannerTransferWaitsForChildLaunches(t *testing.T) {
	e := newSourceEnv(t)
	e.register(t, e.planner(e.collabID, 1001, ""))
	child := SourceRequest{ID: hexID(1003), ParentSourceID: hexID(1001), Provider: "codex", Role: "builder", RunID: e.a.RunID, AttemptID: e.a.AttemptID}
	e.register(t, child)
	if err := e.s.CloseSegment(ctx, hexID(1001), "m4"); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a segment closed while a child it launched was still unlaunched: %v", err)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1003), SessionID: "cx-1", Model: "gpt-a"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.CloseSegment(ctx, hexID(1001), "m4"); err != nil {
		t.Fatalf("closing after the child's launch was recorded: %v", err)
	}
}

func TestChildKeepsOriginalOwner(t *testing.T) {
	e := newSourceEnv(t)
	e.register(t, e.planner(e.collabID, 1001, ""))
	e.register(t, SourceRequest{ID: hexID(1003), ParentSourceID: hexID(1001), Provider: "codex", Role: "builder"})
	got, err := e.s.Source(ctx, hexID(1003))
	if err != nil || got.CollaborationID != e.collabID || got.ParentSourceID != hexID(1001) {
		t.Fatalf("child %+v, %v", got, err)
	}
	// A grandchild inherits through the chain.
	e.register(t, SourceRequest{ID: hexID(1004), ParentSourceID: hexID(1003), Provider: "codex", Role: "subagent"})
	if g, _ := e.s.Source(ctx, hexID(1004)); g.CollaborationID != e.collabID {
		t.Fatalf("grandchild owner %q", g.CollaborationID)
	}
	err = e.s.RegisterSource(ctx, SourceRequest{ID: hexID(1005), CollaborationID: e.collabB, ParentSourceID: hexID(1001),
		Provider: "codex", Role: "builder"})
	if !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a child registered to another collaboration: %v", err)
	}
	// The planner's segment closes and its collaboration stops taking launches;
	// the child launched before still binds and still belongs to A.
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1004), SessionID: "sub-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(`UPDATE collaborations SET status='closing' WHERE id=?`, e.collabID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1003), SessionID: "cx-late"}); err != nil {
		t.Fatalf("a late child could not bind its receipt: %v", err)
	}
	if late, _ := e.s.Source(ctx, hexID(1003)); late.CollaborationID != e.collabID || late.SessionID != "cx-late" {
		t.Fatalf("late child %+v", late)
	}
	err = e.s.RegisterSource(ctx, SourceRequest{ID: hexID(1006), ParentSourceID: hexID(1001), Provider: "codex", Role: "builder"})
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("a launch registered after the collaboration froze: %v", err)
	}
}

func TestSourcePreRunPlannerNeedsNoRunOrAttempt(t *testing.T) {
	e := newSourceEnv(t)
	e.register(t, e.planner(e.collabID, 1001, ""))
	got, err := e.s.Source(ctx, hexID(1001))
	if err != nil || got.RunID != "" || got.AttemptID != "" || got.Status != "bound" {
		t.Fatalf("pre-run planner %+v, %v", got, err)
	}
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: hexID(1002), CollaborationID: e.collabID, Provider: "claude", Role: "planner"}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a planner without a session cannot bound its messages: %v", err)
	}
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: hexID(1007), CollaborationID: e.collabID, Provider: "codex", Role: "builder",
		AttemptID: e.a.AttemptID}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("an attempt without its run: %v", err)
	}
}

func TestSourceUnresolvedSessionStaysExpected(t *testing.T) {
	e := newSourceEnv(t)
	e.register(t, SourceRequest{ID: hexID(1003), CollaborationID: e.collabID, Provider: "codex", Model: "gpt-a", Role: "builder",
		RunID: e.a.RunID, AttemptID: e.a.AttemptID})
	open, err := e.s.UnresolvedSources(ctx, e.collabID)
	if err != nil || len(open) != 1 || open[0].ID != hexID(1003) || open[0].Status != "expected" {
		t.Fatalf("unresolved %+v, %v", open, err)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1003), SessionID: ""}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("binding no session: %v", err)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1003), SessionID: "cx-1", Model: "gpt-b"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1003), SessionID: "cx-1", Model: "gpt-b"}); err != nil {
		t.Fatalf("a repeated identical receipt: %v", err)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1003), SessionID: "cx-2"}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a second, different session for one launch: %v", err)
	}
	if open, _ := e.s.UnresolvedSources(ctx, e.collabID); len(open) != 0 {
		t.Fatalf("still unresolved: %+v", open)
	}
	got, _ := e.s.Source(ctx, hexID(1003))
	if got.Model != "gpt-a" || got.ActualModel != "gpt-b" || !got.ModelMismatch() {
		t.Fatalf("model reconciliation %+v", got)
	}
	if err := e.s.BindSourceSession(ctx, BindRequest{SourceID: hexID(1999), SessionID: "x"}); !errors.Is(err, ErrNoSource) {
		t.Fatalf("binding an unregistered source: %v", err)
	}
}

func TestSourceRegistrationIsIdempotent(t *testing.T) {
	e := newSourceEnv(t)
	req := SourceRequest{ID: hexID(1003), CollaborationID: e.collabID, Provider: "codex", Role: "builder", RunID: e.a.RunID,
		AttemptID: e.a.AttemptID, StartBoundary: "effect-1"}
	e.register(t, req)
	e.register(t, req)
	changed := req
	changed.Role = "reviewer"
	if err := e.s.RegisterSource(ctx, changed); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("the same source id with another role: %v", err)
	}
	for name, bad := range map[string]SourceRequest{
		"bad id":         {ID: "x", CollaborationID: e.collabID, Provider: "codex", Role: "builder"},
		"no provider":    {ID: hexID(1008), CollaborationID: e.collabID, Role: "builder"},
		"no role":        {ID: hexID(1008), CollaborationID: e.collabID, Provider: "codex"},
		"no owner":       {ID: hexID(1008), Provider: "codex", Role: "builder"},
		"unknown parent": {ID: hexID(1008), ParentSourceID: hexID(1999), Provider: "codex", Role: "builder"},
	} {
		if err := e.s.RegisterSource(ctx, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: hexID(1008), CollaborationID: hexID(777), Provider: "codex", Role: "builder"}); !errors.Is(err, ErrWorkMapping) {
		t.Fatalf("an unknown collaboration: %v", err)
	}
}
