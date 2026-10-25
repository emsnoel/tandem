package state

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

func TestReservationDoesNotOverbook(t *testing.T) {
	if reservationFits(100, 70, 20, 11) {
		t.Fatal("overbooked ceiling")
	}
	if !reservationFits(100, 70, 20, 10) {
		t.Fatal("rejected exact ceiling")
	}
	for _, c := range [][4]int64{{-1, 0, 0, 0}, {10, -1, 0, 0}, {10, 0, -1, 0}, {10, 0, 0, -1}, {10, 11, 0, 0}, {10, 5, 6, 0}} {
		if reservationFits(c[0], c[1], c[2], c[3]) {
			t.Errorf("fits %v", c)
		}
	}
}

// admitEnv is an enforced collaboration with a codex budget, a registered
// builder source for the first run, and that run's checkout lease.
type admitEnv struct {
	*collabEnv
	enforced string
	fence    int64
	sources  int
}

const admitOwner = "controller-1"

func newAdmitEnv(t *testing.T, inCeiling int64) *admitEnv {
	t.Helper()
	c := newCollabEnv(t)
	e := &admitEnv{collabEnv: c, enforced: hexID(920)}
	if err := c.s.CreateCollaboration(ctx, CollaborationRequest{ID: e.enforced, PolicyHash: c.policyHash, EnforceBudgets: true,
		InputCeilings: map[string]int64{"codex": inCeiling}, OutputCeilings: map[string]int64{"codex": 1000},
		InputReservations: map[string]int64{"codex": 30}, OutputReservations: map[string]int64{"codex": 10},
		Unlimited: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	fence, err := c.s.AcquireLease(ctx, c.a.CheckoutID, admitOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	e.fence = fence
	return e
}

// source registers a builder source for the first run and returns its id.
func (e *admitEnv) source(t *testing.T) string {
	t.Helper()
	e.sources++
	id := hexID(3000 + e.sources)
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: id, CollaborationID: e.enforced, Provider: "codex", Role: "builder",
		RunID: e.a.RunID, AttemptID: e.a.AttemptID}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *admitEnv) request(t *testing.T, id int, in, out int64) AdmissionRequest {
	t.Helper()
	run, err := e.s.GetRun(ctx, e.a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := e.s.StopGeneration(ctx, e.enforced)
	if err != nil {
		t.Fatal(err)
	}
	return AdmissionRequest{ID: hexID(id), CollaborationID: e.enforced, RunID: e.a.RunID, AttemptID: e.a.AttemptID,
		EffectID: hexID(id + 100), SourceID: e.source(t), Provider: "codex", RunVersion: run.Version, StopGeneration: gen,
		LeaseFence: e.fence, InputReservation: in, OutputReservation: out}
}

func (e *admitEnv) budget(t *testing.T) ProviderBudget {
	t.Helper()
	b, err := e.s.ProviderBudget(ctx, e.enforced, "codex")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdmissionReservesAndIsIdempotentPerEffect(t *testing.T) {
	e := newAdmitEnv(t, 100)
	req := e.request(t, 4001, 30, 10)
	a, err := e.s.AdmitDispatch(ctx, req)
	if err != nil || a.ID != req.ID || a.StopGeneration != req.StopGeneration {
		t.Fatalf("admission %+v %v", a, err)
	}
	if again, err := e.s.AdmitDispatch(ctx, req); err != nil || again != a {
		t.Fatalf("a repeated identical request: %+v %v", again, err)
	}
	if b := e.budget(t); b.InputReserved != 30 || b.OutputReserved != 10 {
		t.Fatalf("reserved %+v: a repeat must not reserve again", b)
	}
	changed := req
	changed.InputReservation = 31
	if _, err := e.s.AdmitDispatch(ctx, changed); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a changed request under the same id: %v", err)
	}
	other := req
	other.ID = hexID(4002)
	if _, err := e.s.AdmitDispatch(ctx, other); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a second admission for the same effect: %v", err)
	}
}

func TestAdmissionRaceForTheLastReservation(t *testing.T) {
	e := newAdmitEnv(t, 50)
	reqs := []AdmissionRequest{e.request(t, 4011, 30, 1), e.request(t, 4012, 30, 1)}
	second, err := Open(ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.WithClock(e.s.now) // the lease was written on the fixture's clock
	stores := []*Store{e.s, second}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range reqs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = stores[i].AdmitDispatch(ctx, reqs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	admitted, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrBudgetExhausted):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != 1 || refused != 1 {
		t.Fatalf("%d admitted, %d refused for one remaining reservation", admitted, refused)
	}
	if b := e.budget(t); b.InputReserved != 30 {
		t.Fatalf("reserved %d after the race", b.InputReserved)
	}
}

func TestAdmissionChecksEveryFence(t *testing.T) {
	e := newAdmitEnv(t, 1000)
	cases := map[string]func(*AdmissionRequest) error{
		"stale stop generation": func(r *AdmissionRequest) error { r.StopGeneration++; return ErrStopped },
		"stale run version":     func(r *AdmissionRequest) error { r.RunVersion++; return workflow.ErrConflict },
		"stale lease fence":     func(r *AdmissionRequest) error { r.LeaseFence++; return workflow.ErrBusy },
		"unregistered source":   func(r *AdmissionRequest) error { r.SourceID = hexID(3999); return ErrNoSource },
		"unknown group":         func(r *AdmissionRequest) error { r.BudgetGroupID = hexID(3998); return ErrWorkMapping },
		"unconfigured provider": func(r *AdmissionRequest) error { r.Provider = "copilot"; return ErrBudgetUnconfigured },
		"over the ceiling":      func(r *AdmissionRequest) error { r.InputReservation = 1001; return ErrBudgetExhausted },
	}
	n := 0
	for name, mutate := range cases {
		n++
		req := e.request(t, 4100+n, 10, 1)
		want := mutate(&req)
		if _, err := e.s.AdmitDispatch(ctx, req); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
	}
	// A source of another collaboration cannot be admitted here.
	other := e.request(t, 4150, 10, 1)
	other.SourceID = hexID(3997)
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: other.SourceID, CollaborationID: e.collabID, Provider: "codex", Role: "builder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AdmitDispatch(ctx, other); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("a source of another collaboration: %v", err)
	}
	// A run that is not running, and an attempt that is not current.
	paused := e.request(t, 4151, 10, 1)
	e.a.exec(t, `UPDATE runs SET state='paused' WHERE id=?`, e.a.RunID)
	if _, err := e.s.AdmitDispatch(ctx, paused); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("a paused run: %v", err)
	}
	e.a.exec(t, `UPDATE runs SET state='running' WHERE id=?`, e.a.RunID)
	stale := e.request(t, 4152, 10, 1)
	e.a.exec(t, `UPDATE attempts SET is_current=0 WHERE id=?`, e.a.AttemptID)
	if _, err := e.s.AdmitDispatch(ctx, stale); !errors.Is(err, workflow.ErrStaleAttempt) {
		t.Errorf("an attempt that is not current: %v", err)
	}
	var admissions int
	e.s.db.QueryRow(`SELECT count(*) FROM dispatch_admissions`).Scan(&admissions)
	if b := e.budget(t); admissions != 0 || b.InputReserved != 0 {
		t.Fatalf("refused requests wrote %d admissions and reserved %d", admissions, b.InputReserved)
	}
}

func TestAdmissionUnlimitedDiffersFromUnconfigured(t *testing.T) {
	e := newAdmitEnv(t, 100)
	req := e.request(t, 4201, 1<<40, 1<<40)
	req.Provider = "claude"
	if _, err := e.s.AdmitDispatch(ctx, req); err != nil {
		t.Fatalf("an explicitly unlimited provider: %v", err)
	}
	req = e.request(t, 4202, 1, 1)
	req.Provider = "opencode"
	if _, err := e.s.AdmitDispatch(ctx, req); !errors.Is(err, ErrBudgetUnconfigured) {
		t.Fatalf("a provider with no budget declared: %v", err)
	}
	// A collaboration that does not enforce budgets admits nothing: its runs
	// register sources and collect usage without reservations.
	plain := e.request(t, 4203, 1, 1)
	plain.CollaborationID = e.collabID
	plain.SourceID = hexID(3996)
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: plain.SourceID, CollaborationID: e.collabID, Provider: "codex", Role: "builder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AdmitDispatch(ctx, plain); !errors.Is(err, ErrNotEnforced) {
		t.Fatalf("admission in a collaboration without budget enforcement: %v", err)
	}
}

func TestAdmissionReplacementRunsShareTheCeiling(t *testing.T) {
	e := newAdmitEnv(t, 50)
	if _, err := e.s.AdmitDispatch(ctx, e.request(t, 4301, 30, 1)); err != nil {
		t.Fatal(err)
	}
	// The replacement run has its own checkout lease and source, and the same
	// collaboration budget.
	e.b.exec(t, `UPDATE runs SET state='running' WHERE id=?`, e.b.RunID)
	fence, err := e.s.AcquireLease(ctx, e.b.CheckoutID, admitOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	src := hexID(3990)
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: src, CollaborationID: e.enforced, Provider: "codex", Role: "builder",
		RunID: e.b.RunID, AttemptID: e.b.AttemptID}); err != nil {
		t.Fatal(err)
	}
	run, _ := e.s.GetRun(ctx, e.b.RunID)
	_, err = e.s.AdmitDispatch(ctx, AdmissionRequest{ID: hexID(4302), CollaborationID: e.enforced, RunID: e.b.RunID, AttemptID: e.b.AttemptID,
		EffectID: hexID(4402), SourceID: src, Provider: "codex", RunVersion: run.Version, LeaseFence: fence, InputReservation: 30, OutputReservation: 1})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("a replacement run got a fresh allowance: %v", err)
	}
}

func TestAdmissionSettlementReconcilesAndRecordsOvershoot(t *testing.T) {
	e := newAdmitEnv(t, 100)
	a1, _ := e.s.AdmitDispatch(ctx, e.request(t, 4401, 30, 10))
	a2, _ := e.s.AdmitDispatch(ctx, e.request(t, 4402, 30, 10))
	// Final usage known: the reservation becomes spend.
	if err := e.s.SettleAdmission(ctx, SettleRequest{AdmissionID: a1.ID, InputUsed: int64p(25), OutputUsed: int64p(4)}); err != nil {
		t.Fatal(err)
	}
	// An unknown tail keeps its reservation: it cannot be spent again.
	if err := e.s.SettleAdmission(ctx, SettleRequest{AdmissionID: a2.ID}); err != nil {
		t.Fatal(err)
	}
	if b := e.budget(t); b.InputSpent != 25 || b.InputReserved != 30 || b.OutputSpent != 4 || b.OutputReserved != 10 {
		t.Fatalf("after settling: %+v", b)
	}
	if _, err := e.s.AdmitDispatch(ctx, e.request(t, 4403, 46, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("the unknown tail's reservation was given away: %v", err)
	}
	// The tail's usage arrives late and overshoots the ceiling.
	if err := e.s.SettleAdmission(ctx, SettleRequest{AdmissionID: a2.ID, InputUsed: int64p(90), OutputUsed: int64p(5)}); err != nil {
		t.Fatal(err)
	}
	b := e.budget(t)
	if b.InputSpent != 115 || b.InputReserved != 0 || b.InputOvershoot != 15 {
		t.Fatalf("overshoot not recorded: %+v", b)
	}
	if _, err := e.s.AdmitDispatch(ctx, e.request(t, 4404, 0, 0)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("admission after an overshoot: %v", err)
	}
	if err := e.s.SettleAdmission(ctx, SettleRequest{AdmissionID: a1.ID, InputUsed: int64p(26), OutputUsed: int64p(4)}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("settling a settled admission with other usage: %v", err)
	}
}

func TestAdmissionReleaseOnlyForUndeliveredEffects(t *testing.T) {
	e := newAdmitEnv(t, 100)
	req := e.request(t, 4501, 30, 10)
	a, _ := e.s.AdmitDispatch(ctx, req)
	payload := e.a.artifact(t, "dispatch payload")
	e.a.exec(t, `INSERT INTO events(id,run_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?,'x',?,0,1,1)`, hexID(4601), e.a.RunID, payload)
	e.a.exec(t, `INSERT INTO effects(id,run_id,source_event_id,kind,payload_hash,nonce,state,available_ms,created_ms)
		VALUES(?,?,?,'builder_dispatch',?,'n','delivery_unknown',1,1)`, req.EffectID, e.a.RunID, hexID(4601), payload)
	if err := e.s.ReleaseAdmission(ctx, a.ID); !errors.Is(err, workflow.ErrDeliveryUnknown) {
		t.Fatalf("releasing a reservation whose delivery is unknown: %v", err)
	}
	e.a.exec(t, `UPDATE effects SET state='failed' WHERE id=?`, req.EffectID)
	if err := e.s.ReleaseAdmission(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if b := e.budget(t); b.InputReserved != 0 || b.OutputReserved != 0 {
		t.Fatalf("after release %+v", b)
	}
}

func TestAdmissionBudgetAmendmentNeedsAnAuditedReason(t *testing.T) {
	e := newAdmitEnv(t, 50)
	if err := e.s.AmendBudget(ctx, AmendRequest{CollaborationID: e.enforced, Provider: "codex", InputCeiling: int64p(80)}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("an amendment without actor and reason: %v", err)
	}
	if err := e.s.AmendBudget(ctx, AmendRequest{CollaborationID: e.enforced, Provider: "codex", InputCeiling: int64p(80),
		Actor: "user", Reason: "repair needs room"}); err != nil {
		t.Fatal(err)
	}
	if b := e.budget(t); b.InputCeiling == nil || *b.InputCeiling != 80 {
		t.Fatalf("ceiling %+v", b)
	}
	var audits int
	e.s.db.QueryRow(`SELECT count(*) FROM collaboration_audit WHERE action='amend_budget' AND reason='repair needs room'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("%d audit rows", audits)
	}
}

func int64p(v int64) *int64 { return &v }
