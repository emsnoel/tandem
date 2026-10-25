package controller

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
)

const testCollab = "0000000000000000000000000000c011"

// startInCollaboration creates an open collaboration and starts the env's run
// attached to it.
func (e *cenv) startInCollaboration() string {
	e.t.Helper()
	policy, err := e.c.put(ctx, `{"input_ceilings":{"codex":100000}}`)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy}); err != nil {
		e.t.Fatal(err)
	}
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.Collaboration = testCollab }))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
	return id
}

// tickUntilSent ticks until the transport has accepted n sends.
func (e *cenv) tickUntilSent(n int) {
	e.t.Helper()
	for i := 0; i < 10 && e.sent() < n; i++ {
		if err := e.c.Tick(ctx, e.runID); err != nil {
			e.t.Fatalf("tick: %v", err)
		}
		e.clk.advance(2 * time.Second)
	}
	if e.sent() < n {
		e.t.Fatalf("%d sends after ten ticks, want %d", e.sent(), n)
	}
}

func (e *cenv) sent() int {
	e.tr.mu.Lock()
	defer e.tr.mu.Unlock()
	return len(e.tr.sends)
}

// raw opens the env's database directly, for what no state method does yet.
func (e *cenv) raw() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.stRoot, "state.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	return db
}

func (e *cenv) sourceCount() int {
	e.t.Helper()
	var n int
	if err := e.raw().QueryRow(`SELECT count(*) FROM expected_sources`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestSourceRegisteredBeforeTransport(t *testing.T) {
	e := newCEnv(t)
	var unregistered []string
	e.tr.sendErr = func(d runner.Delivery) error {
		if launchRole(d.Kind) != "" {
			if _, err := e.st.Source(ctx, sourceID(d.EffectID)); err != nil {
				unregistered = append(unregistered, d.Kind+" "+d.EffectID+": "+err.Error())
			}
		}
		return nil
	}
	e.startInCollaboration()
	e.tickUntilSent(1)
	if len(unregistered) > 0 {
		t.Fatalf("sends reached the transport before their source was registered: %v", unregistered)
	}
	d := e.tr.sends[0]
	src, err := e.st.Source(ctx, sourceID(d.EffectID))
	if err != nil {
		t.Fatal(err)
	}
	if src.CollaborationID != testCollab || src.Role != "builder" || src.Provider != "codex" || src.RunID != e.runID ||
		src.AttemptID != d.AttemptID || src.SessionID != "session-"+d.EffectID[:6] || src.Status != "bound" {
		t.Fatalf("source %+v for delivery %+v", src, d)
	}
}

func TestSourceRegistrationFailurePreventsSend(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	if _, err := e.raw().Exec(`UPDATE collaborations SET status='closing' WHERE id=?`, testCollab); err != nil {
		t.Fatal(err)
	}
	// A frozen collaboration holds the launch: no send, no registration, and
	// the dispatch is not counted as a failed send.
	if err := e.ticks(5); err != nil {
		t.Fatalf("ticks while frozen: %v", err)
	}
	e.held()
	if e.sourceCount() != 0 {
		t.Fatalf("%d sources after a refused registration", e.sourceCount())
	}
}

func TestSourceRetriedSendReusesItsRegistration(t *testing.T) {
	e := newCEnv(t)
	failures := 1
	e.tr.sendErr = func(d runner.Delivery) error {
		if failures > 0 {
			failures--
			return runner.NotSent(errors.New("the pane was not there"))
		}
		return nil
	}
	e.startInCollaboration()
	e.tickUntilSent(1)
	if e.tr.attempts != 2 || e.sourceCount() != 1 {
		t.Fatalf("%d transport calls, %d sources: a retried send must reuse its registration", e.tr.attempts, e.sourceCount())
	}
}

func TestSourceUnknownReceiptStaysUnresolvedAcrossRestart(t *testing.T) {
	e := newCEnv(t)
	e.tr.noSession = true
	e.startInCollaboration()
	e.tickUntilSent(1)
	open, err := e.st.UnresolvedSources(ctx, testCollab)
	if err != nil || len(open) != 1 || open[0].SessionID != "" {
		t.Fatalf("unresolved %+v, %v", open, err)
	}
	before := e.sent()
	e.c = e.newController()
	for i := 0; i < 3; i++ {
		if err := e.c.Tick(ctx, e.runID); err != nil && !strings.Contains(err.Error(), "writer") {
			t.Fatalf("tick after restart: %v", err)
		}
		e.clk.advance(2 * time.Second)
	}
	if e.sent() != before || e.sourceCount() != 1 {
		t.Fatalf("after a restart: %d sends (was %d), %d sources", e.sent(), before, e.sourceCount())
	}
	if open, _ := e.st.UnresolvedSources(ctx, testCollab); len(open) != 1 {
		t.Fatalf("a session was invented for the unknown receipt: %+v", open)
	}
}

func TestSourceRunWithoutCollaborationRegistersNothing(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tickUntilSent(1)
	if e.sourceCount() != 0 {
		t.Fatalf("a run without a collaboration registered %d sources", e.sourceCount())
	}
	run, err := e.st.GetRun(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := e.c.config(ctx, run)
	if err != nil || strings.Contains(string(encode(cfg)), "collaboration") {
		t.Fatalf("the run configuration of a run without a collaboration changed: %s %v", encode(cfg), err)
	}
}

func TestSourceStartNeedsAnOpenCollaboration(t *testing.T) {
	e := newCEnv(t)
	_, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderSession, r.PlannerSession, r.Collaboration = "b", "p", testCollab }))
	if !errors.Is(err, state.ErrWorkMapping) {
		t.Fatalf("a run for an unknown collaboration: %v", err)
	}
}
