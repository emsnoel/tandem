package runner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

var ctx = context.Background()

func hex32(n int) string { return fmt.Sprintf("%032x", n) }

// fakeTransport records every send and answers from a script. It stands in
// for Herdr and the agent CLIs; the delivery contract is what is under test.
type fakeTransport struct {
	mu       sync.Mutex
	sends    []Delivery
	sendErrs []error // consumed in order; nil entries and an empty list succeed
	receipt  Receipt
	obs      Observation
	obsErr   error
	observed int
	// during, when set, runs inside Send before it returns: an agent that
	// acknowledges its prompt while the transport is still watching the pane.
	during func()
	// entered is signalled when a send starts, and release lets it finish, so a
	// test can pause a worker inside Send.
	entered chan struct{}
	release chan struct{}
}

func (f *fakeTransport) Send(ctx context.Context, d Delivery) (Receipt, error) {
	f.mu.Lock()
	f.sends = append(f.sends, d)
	var err error
	if len(f.sendErrs) > 0 {
		err, f.sendErrs = f.sendErrs[0], f.sendErrs[1:]
	}
	r := f.receipt
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during()
	}
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.release
	}
	if err != nil {
		return Receipt{}, err
	}
	r.EffectID = d.EffectID
	return r, nil
}

func (f *fakeTransport) Observe(ctx context.Context, effectID string) (Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed++
	return f.obs, f.obsErr
}

func (f *fakeTransport) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

type clock struct{ ms int64 }

func (c *clock) now() time.Time          { return time.UnixMilli(c.ms) }
func (c *clock) advance(d time.Duration) { c.ms += d.Milliseconds() }

type env struct {
	st    *state.Store
	art   *artifact.Store
	root  string
	clk   *clock
	effID string
	nonce string
}

// newEnv opens a real store and seeds one run with one pending effect, using
// plain SQL with foreign keys on.
func newEnv(t *testing.T) *env {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	st, err := state.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	art, err := artifact.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, art: art, root: root, clk: &clock{ms: 5_000_000}, effID: hex32(500)}
	st.WithClock(e.clk.now)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	put := func(s string) string {
		h, err := art.Put(ctx, strings.NewReader(s))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,'text/plain',1)`, h, len(s)); err != nil {
			t.Fatal(err)
		}
		return h
	}
	prompt, plan, cfg := put("the prompt"), put("plan"), put("config")
	e.nonce = hex32(777)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?, '/c', '/c/.git', 1)`, []any{hex32(1)}},
		{`INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?, '.')`, []any{hex32(2), hex32(1)}},
		{`INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,created_ms) VALUES(?,?,?,?,2,'running',1)`, []any{hex32(3), hex32(2), plan, cfg}},
		{`INSERT INTO events(id,run_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?, 'stop_requested',?,0,1,1)`, []any{hex32(4), hex32(3), prompt}},
		{`INSERT INTO effects(id,run_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms) VALUES(?,?,?, 'builder_dispatch',0,?,?, 'pending',0,?,?)`,
			[]any{e.effID, hex32(3), hex32(4), prompt, e.nonce, e.clk.ms - 1, e.clk.ms - 1}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %q: %v", q.sql, err)
		}
	}
	return e
}

func (e *env) deliverer(tr Transport, owner string) *Deliverer {
	return &Deliverer{Store: e.st, Transport: tr, Artifacts: e.art, Owner: owner, RunID: hex32(3), AgentKind: "codex"}
}

func (e *env) effect(t *testing.T) workflow.Effect {
	t.Helper()
	eff, err := e.st.GetEffect(ctx, e.effID)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}

func (e *env) receiptHash(t *testing.T) *string {
	t.Helper()
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(e.root, "state.sqlite"))
	defer db.Close()
	var h *string
	db.QueryRow(`SELECT receipt_hash FROM effects WHERE id=?`, e.effID).Scan(&h)
	return h
}

func TestTickSendsTheClaimedEffectOnceAndRecordsTheReceipt(t *testing.T) {
	e := newEnv(t)
	tr := &fakeTransport{receipt: Receipt{ExternalID: "session-1"}}
	out, err := e.deliverer(tr, "w1").Tick(ctx)
	if err != nil || out.Kind != Sent || out.EffectID != e.effID {
		t.Fatalf("%+v %v", out, err)
	}
	if tr.sendCount() != 1 {
		t.Fatalf("%d sends", tr.sendCount())
	}
	d := tr.sends[0]
	if d.EffectID != e.effID || d.Nonce != e.nonce || d.AgentKind != "codex" || len(d.PromptHash) != 64 || d.AttemptID != "" {
		t.Fatalf("delivery %+v", d)
	}
	if h := e.receiptHash(t); h == nil {
		t.Fatal("the receipt was not recorded")
	} else if raw, err := e.art.ReadAll(ctx, *h, 4096); err != nil || !strings.Contains(string(raw), "session-1") || !strings.Contains(string(raw), e.nonce) {
		t.Fatalf("receipt artifact %q %v", raw, err)
	}
	if out, err := e.deliverer(tr, "w1").Tick(ctx); err != nil || out.Kind != Idle {
		t.Fatalf("with nothing to do: %+v %v", out, err)
	}
}

// A crash at any point after the claim may have followed a real send, so the
// next owner reconciles and never sends again.
func TestSendCrashAfterDeliveryDoesNotResend(t *testing.T) {
	for _, point := range []string{"before_send", "after_send_before_receipt"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t)
			tr := &fakeTransport{}
			crash := errors.New("process died at " + point)
			w1 := e.deliverer(tr, "w1")
			switch point {
			case "before_send":
				w1.hooks.beforeSend = func() error { return crash }
			default:
				w1.hooks.afterSend = func() error { return crash }
			}
			if _, err := w1.Tick(ctx); !errors.Is(err, crash) {
				t.Fatalf("err = %v", err)
			}
			wantSends := 1
			if point == "before_send" {
				wantSends = 0
			}
			if tr.sendCount() != wantSends {
				t.Fatalf("%d sends before the crash was observed", tr.sendCount())
			}
			if st := e.effect(t).State; st != "delivering" {
				t.Fatalf("the dead worker's claim is %s", st)
			}

			// While the lease is live nobody else touches it.
			if out, _ := e.deliverer(tr, "w2").Tick(ctx); out.Kind != Idle {
				t.Fatalf("a live lease was taken: %+v", out)
			}
			e.clk.advance(31 * time.Second)
			for i := 0; i < 3; i++ {
				out, err := e.deliverer(tr, "w2").Tick(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if out.Kind != StillUnknown && out.Kind != Idle {
					t.Fatalf("tick %d: %+v", i, out)
				}
				e.clk.advance(31 * time.Second)
			}
			if tr.sendCount() != wantSends {
				t.Fatalf("%d sends after recovery, want %d: an uncertain delivery was sent again", tr.sendCount(), wantSends)
			}
			if st := e.effect(t).State; st != "delivery_unknown" {
				t.Fatalf("state %s, want delivery_unknown", st)
			}
			if tr.observed == 0 {
				t.Fatal("recovery never asked the transport what happened")
			}
		})
	}
}

func TestAmbiguousSendErrorGoesStraightToUnknownWithEvidence(t *testing.T) {
	e := newEnv(t)
	tr := &fakeTransport{sendErrs: []error{errors.New("connection reset after the write")}}
	out, err := e.deliverer(tr, "w").Tick(ctx)
	if err != nil || out.Kind != Unknown {
		t.Fatalf("%+v %v", out, err)
	}
	if eff := e.effect(t); eff.State != "delivery_unknown" {
		t.Fatalf("state %s", eff.State)
	}
	h := e.receiptHash(t)
	if h == nil {
		t.Fatal("the transport's evidence was not retained")
	}
	if raw, _ := e.art.ReadAll(ctx, *h, 4096); !strings.Contains(string(raw), "connection reset") {
		t.Fatalf("evidence %q", raw)
	}
	for i := 0; i < 4; i++ {
		e.clk.advance(time.Minute)
		e.deliverer(tr, "w").Tick(ctx)
	}
	if tr.sendCount() != 1 {
		t.Fatalf("an unknown send was repeated: %d sends", tr.sendCount())
	}
}

func TestDefiniteNotSentHasBoundedRetries(t *testing.T) {
	e := newEnv(t)
	down := NotSent(errors.New("herdr socket refused the connection"))
	tr := &fakeTransport{sendErrs: []error{down, down, down, down}}
	w := e.deliverer(tr, "w")
	wantKinds := []OutcomeKind{NotSentRetry, NotSentRetry, Failed}
	for i, want := range wantKinds {
		out, err := w.Tick(ctx)
		if err != nil || out.Kind != want {
			t.Fatalf("attempt %d: %+v %v, want %v", i+1, out, err, want)
		}
		if i < 2 {
			// Backoff: nothing happens until its time.
			if idle, _ := w.Tick(ctx); idle.Kind != Idle {
				t.Fatalf("retried before the backoff: %+v", idle)
			}
			e.clk.advance(time.Second << i)
		}
	}
	if tr.sendCount() != 3 {
		t.Fatalf("%d sends, want 3", tr.sendCount())
	}
	for _, d := range tr.sends {
		if d.EffectID != e.effID || d.Nonce != e.nonce {
			t.Fatalf("a retry changed the effect identity: %+v", d)
		}
	}
	if st := e.effect(t).State; st != "failed" {
		t.Fatalf("state %s", st)
	}
	e.clk.advance(time.Hour)
	if out, _ := w.Tick(ctx); out.Kind != Idle || tr.sendCount() != 3 {
		t.Fatalf("a failed effect was sent again: %+v", out)
	}
}

func TestExpiredFenceCannotRecordReceipt(t *testing.T) {
	e := newEnv(t)
	slow := &fakeTransport{entered: make(chan struct{}), release: make(chan struct{}), receipt: Receipt{ExternalID: "late"}}
	oldDone := make(chan error, 1)
	var oldOut Outcome
	go func() {
		var err error
		oldOut, err = e.deliverer(slow, "paused-worker").Tick(ctx)
		oldDone <- err
	}()
	<-slow.entered // the old worker is inside Send, holding fence 1

	e.clk.advance(31 * time.Second)
	fast := &fakeTransport{}
	out, err := e.deliverer(fast, "new-worker").Tick(ctx)
	if err != nil || out.Kind != StillUnknown {
		t.Fatalf("new worker: %+v %v", out, err)
	}
	if fast.sendCount() != 0 {
		t.Fatal("the new owner sent a prompt that the old one may already have sent")
	}
	fence := e.effect(t).Fence

	close(slow.release) // the old worker wakes with its receipt
	err = <-oldDone
	if !errors.Is(err, state.ErrStaleFence) || !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("the old worker's receipt was not refused: %v (outcome %+v)", err, oldOut)
	}
	if eff := e.effect(t); eff.State != "delivery_unknown" || eff.Fence != fence {
		t.Fatalf("the refused write changed the effect: %+v", eff)
	}
	if e.receiptHash(t) != nil {
		t.Fatal("the stale receipt was stored")
	}
}

// Starting the process proves the process started. Only the nonce coming back
// proves the prompt was read.
func TestProcessStartIsNotPromptAcknowledgement(t *testing.T) {
	e := newEnv(t)
	wrapper := &fakeTransport{receipt: Receipt{ExternalID: "pid-4242"}} // a launch wrapper's receipt
	if out, err := e.deliverer(wrapper, "w").Tick(ctx); err != nil || out.Kind != Sent {
		t.Fatalf("%+v %v", out, err)
	}
	if st := e.effect(t).State; st == "acknowledged" {
		t.Fatal("an accepted send was treated as acknowledged")
	}

	// The process is running, but it has not acted on this prompt's nonce.
	wrapper.obs = Observation{State: ObsRunning, Nonce: "", ExternalID: "pid-4242"}
	e.clk.advance(state.AckWindow + time.Second)
	for i := 0; i < 2; i++ {
		out, err := e.deliverer(wrapper, "w").Tick(ctx)
		if err != nil || out.Kind != StillUnknown {
			t.Fatalf("a running process with no nonce: %+v %v", out, err)
		}
		e.clk.advance(time.Minute)
	}
	wrapper.obs = Observation{State: ObsRunning, Nonce: hex32(1), ExternalID: "pid-4242"} // someone else's nonce
	if out, _ := e.deliverer(wrapper, "w").Tick(ctx); out.Kind != StillUnknown {
		t.Fatalf("another prompt's nonce confirmed this one: %+v", out)
	}
	if st := e.effect(t).State; st != "delivery_unknown" {
		t.Fatalf("state %s: delivery must stay unconfirmed", st)
	}

	e.clk.advance(time.Minute)
	wrapper.obs = Observation{State: ObsRunning, Nonce: e.nonce, ExternalID: "pid-4242"}
	out, err := e.deliverer(wrapper, "w").Tick(ctx)
	if err != nil || out.Kind != Acknowledged {
		t.Fatalf("the matching nonce: %+v %v", out, err)
	}
	if st := e.effect(t).State; st != "acknowledged" {
		t.Fatalf("state %s", st)
	}
	if wrapper.sendCount() != 1 {
		t.Fatalf("%d sends", wrapper.sendCount())
	}
}

func TestStoppedOrUnknownObservationNeverAcknowledges(t *testing.T) {
	e := newEnv(t)
	tr := &fakeTransport{sendErrs: []error{errors.New("ambiguous")}}
	e.deliverer(tr, "w").Tick(ctx)
	for _, obs := range []Observation{
		{State: ObsStopped, Nonce: e.nonce}, {State: ObsUnknown, Nonce: e.nonce}, {State: ObsCompleted, Nonce: "other"},
	} {
		tr.obs = obs
		e.clk.advance(time.Minute)
		if out, _ := e.deliverer(tr, "w").Tick(ctx); out.Kind == Acknowledged {
			t.Fatalf("observation %+v acknowledged the effect", obs)
		}
	}
	tr.obsErr = errors.New("herdr unreachable")
	e.clk.advance(time.Minute)
	if out, err := e.deliverer(tr, "w").Tick(ctx); err == nil || out.Kind != StillUnknown {
		t.Fatalf("an observation failure must be reported and leave it unknown: %+v %v", out, err)
	}
	if tr.sendCount() != 1 {
		t.Fatalf("%d sends", tr.sendCount())
	}
}

func TestHeartbeatKeepsALongSendLeased(t *testing.T) {
	e := newEnv(t)
	slow := &fakeTransport{entered: make(chan struct{}), release: make(chan struct{})}
	fire := make(chan time.Time)
	beat := make(chan struct{}, 4)
	w := e.deliverer(slow, "w")
	w.After = func(d time.Duration) <-chan time.Time {
		if d != state.HeartbeatInterval {
			t.Errorf("heartbeat interval %v", d)
		}
		return fire
	}
	w.hooks.afterHeartbeat = func(err error) {
		if err != nil {
			t.Errorf("heartbeat: %v", err)
		}
		beat <- struct{}{}
	}
	done := make(chan error, 1)
	go func() { _, err := w.Tick(ctx); done <- err }()
	<-slow.entered
	e.clk.advance(12 * time.Second)
	fire <- time.Time{}
	<-beat
	e.clk.advance(25 * time.Second) // 37 s after the claim: only the heartbeat keeps it live
	if out, _ := e.deliverer(&fakeTransport{}, "other").Tick(ctx); out.Kind != Idle {
		t.Fatalf("a heartbeated send was taken over: %+v", out)
	}
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := e.effect(t).State; st != "delivering" {
		t.Fatalf("state %s", st)
	}
}

func TestTransportErrorsAreUncertainUnlessMarkedNotSent(t *testing.T) {
	if IsNotSent(errors.New("boom")) || IsNotSent(nil) || IsNotSent(context.Canceled) {
		t.Fatal("an unclassified error must be treated as uncertain")
	}
	wrapped := fmt.Errorf("while sending: %w", NotSent(errors.New("refused")))
	if !IsNotSent(wrapped) || !errors.Is(wrapped, ErrNotSent) {
		t.Fatal("a marked error survives wrapping")
	}
}

// BeforeSend is the last step before a send: when it fails, nothing is sent,
// and the effect is a definite not-sent that may be retried with its identity.
func TestBeforeSendFailurePreventsTheSend(t *testing.T) {
	e := newEnv(t)
	tr := &fakeTransport{receipt: Receipt{ExternalID: "session-1"}}
	w := e.deliverer(tr, "w")
	var seen []Delivery
	refuse := errors.New("the source could not be registered")
	w.BeforeSend = func(_ context.Context, eff workflow.Effect, d Delivery) error {
		if eff.ID != e.effID || d.EffectID != e.effID || d.Nonce != e.nonce {
			t.Errorf("BeforeSend saw %+v / %+v", eff, d)
		}
		seen = append(seen, d)
		if len(seen) == 1 {
			return refuse
		}
		return nil
	}
	out, err := w.Tick(ctx)
	if !errors.Is(err, refuse) || out.Kind != NotSentRetry || tr.sendCount() != 0 {
		t.Fatalf("a refused BeforeSend: %+v %v, %d sends", out, err, tr.sendCount())
	}
	e.clk.advance(time.Second)
	if out, err := w.Tick(ctx); err != nil || out.Kind != Sent || tr.sendCount() != 1 || len(seen) != 2 {
		t.Fatalf("the retry: %+v %v, %d sends, %d hook calls", out, err, tr.sendCount(), len(seen))
	}
}

// A hold is not a failure: the effect goes back to pending with its retries
// untouched, however often it is held.
func TestBeforeSendHoldReturnsTheEffectUntouched(t *testing.T) {
	e := newEnv(t)
	tr := &fakeTransport{receipt: Receipt{ExternalID: "session-1"}}
	w := e.deliverer(tr, "w")
	holds := 5
	w.BeforeSend = func(context.Context, workflow.Effect, Delivery) error {
		if holds > 0 {
			holds--
			return Hold(errors.New("the collaboration is stopped"))
		}
		return nil
	}
	for i := 0; i < 5; i++ {
		out, err := w.Tick(ctx)
		if !errors.Is(err, ErrHold) || out.Kind != Held {
			t.Fatalf("hold %d: %+v %v", i, out, err)
		}
		if eff := e.effect(t); eff.State != "pending" {
			t.Fatalf("a held effect is %s", eff.State)
		}
	}
	if out, err := w.Tick(ctx); err != nil || out.Kind != Sent || tr.sendCount() != 1 {
		t.Fatalf("after five holds, more than the three permitted failures: %+v %v", out, err)
	}
}

// A send that is definitely not sent keeps its reason: a pane whose turn ended
// at once says why, and the run that pauses for it must be able to say
// so. Each attempt keeps its own; the effect that failed holds the last.
func TestDefiniteNotSentKeepsItsReason(t *testing.T) {
	e := newEnv(t)
	tr := &fakeTransport{sendErrs: []error{
		NotSent(errors.New("first: the turn ended as soon as it started")),
		NotSent(errors.New("second: the turn ended as soon as it started")),
		NotSent(errors.New("There's an issue with the selected model (sonnet-5.5).")),
	}}
	w := e.deliverer(tr, "w")
	reason := func() string {
		t.Helper()
		h, ok, err := e.st.EffectNotSentReason(ctx, e.effID)
		if err != nil || !ok {
			t.Fatalf("no reason kept: %v %v", ok, err)
		}
		b, err := e.art.ReadAll(ctx, h, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if i == 0 && !strings.Contains(reason(), "first:") {
			t.Errorf("after one attempt: %q", reason())
		}
		e.clk.advance(4 * time.Second)
	}
	if st := e.effect(t).State; st != "failed" {
		t.Fatalf("state %s", st)
	}
	if r := reason(); !strings.Contains(r, "issue with the selected model") {
		t.Errorf("the failed effect's reason is %q, want the last attempt's", r)
	}
}

// An agent can acknowledge its prompt before the transport finishes confirming
// the send (the herdr transport watches for 8 seconds). The acknowledgement
// proves delivery, so the send counts and is never retried.
func TestAnAcknowledgementDuringTheSendCountsAsSent(t *testing.T) {
	for name, sendErr := range map[string]error{
		"the transport confirmed it":             nil,
		"the transport saw the turn end at once": NotSent(errors.New("the turn ended as soon as it started")),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			tr := &fakeTransport{receipt: Receipt{ExternalID: "s1"}}
			if sendErr != nil {
				tr.sendErrs = []error{sendErr}
			}
			tr.during = func() {
				if err := e.st.AcknowledgeEffect(ctx, e.effID, e.nonce); err != nil {
					t.Errorf("acknowledge: %v", err)
				}
			}
			out, err := e.deliverer(tr, "w").Tick(ctx)
			if err != nil || out.Kind != Sent {
				t.Fatalf("outcome %+v err %v, want sent", out, err)
			}
			if st := e.effect(t).State; st != "acknowledged" {
				t.Errorf("state %s, want acknowledged", st)
			}
			e.clk.advance(time.Hour)
			if out, _ := e.deliverer(tr, "w").Tick(ctx); out.Kind != Idle || tr.sendCount() != 1 {
				t.Errorf("an acknowledged effect was sent again: %+v, %d sends", out, tr.sendCount())
			}
		})
	}
}
