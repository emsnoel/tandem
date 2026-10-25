package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const enforcedCollab = "0000000000000000000000000000e0f0"

// startEnforced starts the env's run in a collaboration that enforces a codex
// budget: inCeiling input tokens, 60 reserved per launch.
func (e *cenv) startEnforced(inCeiling int64) {
	e.t.Helper()
	policy, err := e.c.put(ctx, `{"enforce_budgets":true}`)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: enforcedCollab, PolicyHash: policy, EnforceBudgets: true,
		InputCeilings: map[string]int64{"codex": inCeiling}, OutputCeilings: map[string]int64{"codex": 1000},
		InputReservations: map[string]int64{"codex": 60}, OutputReservations: map[string]int64{"codex": 5}}); err != nil {
		e.t.Fatal(err)
	}
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.Collaboration = enforcedCollab }))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
}

func (e *cenv) codexBudget() state.ProviderBudget {
	e.t.Helper()
	b, err := e.st.ProviderBudget(ctx, enforcedCollab, "codex")
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// ticks runs n ticks and returns the last error.
func (e *cenv) ticks(n int) error {
	var err error
	for i := 0; i < n; i++ {
		err = e.c.Tick(ctx, e.runID)
		e.clk.advance(2 * time.Second)
	}
	return err
}

// held asserts the run is paused for scheduling with its dispatch pending and
// no send failure counted against it.
func (e *cenv) held() {
	e.t.Helper()
	run, err := e.st.GetRun(ctx, e.runID)
	if err != nil || run.State != "paused" || run.PauseCode != workflow.PauseScheduling {
		e.t.Fatalf("run %s/%s, %v: a held launch pauses the run for scheduling", run.State, run.PauseCode, err)
	}
	var pending, retries int
	if err := e.raw().QueryRow(`SELECT count(*), COALESCE(max(retry_count),0) FROM effects WHERE kind='builder_dispatch' AND state='pending'`).
		Scan(&pending, &retries); err != nil || pending != 1 || retries != 0 {
		e.t.Fatalf("%d pending dispatches with %d retries, %v: a hold is not a failed send", pending, retries, err)
	}
	if e.tr.attempts != 0 {
		e.t.Fatalf("%d transport calls for a launch that was held", e.tr.attempts)
	}
}

func TestAdmissionGatesTheLaunchAndAnAmendmentReopensIt(t *testing.T) {
	e := newCEnv(t)
	e.startEnforced(50) // less than one launch reserves
	if err := e.ticks(5); err != nil {
		t.Fatalf("ticks while held: %v", err)
	}
	e.held()
	if err := e.st.AmendBudget(ctx, state.AmendRequest{CollaborationID: enforcedCollab, Provider: "codex", InputCeiling: int64Ptr(500),
		Actor: "user", Reason: "first task needs room"}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.ResumeHeld(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.tickUntilSent(1)
	if b := e.codexBudget(); b.InputReserved != 60 || b.OutputReserved != 5 {
		t.Fatalf("after the admitted launch: %+v", b)
	}
	flight, err := e.st.InFlightAdmissions(ctx, enforcedCollab)
	if err != nil || len(flight) != 1 || flight[0].ID != admissionID(e.tr.sends[0].EffectID) {
		t.Fatalf("in flight %+v, %v", flight, err)
	}
}

func TestAdmissionStopFirstSendsNothing(t *testing.T) {
	e := newCEnv(t)
	e.startEnforced(1000)
	if err := e.st.StopCollaboration(ctx, state.ControlRequest{CollaborationID: enforcedCollab, Actor: "user", Reason: "hold"}); err != nil {
		t.Fatal(err)
	}
	if err := e.ticks(3); err != nil {
		t.Fatalf("ticks while stopped: %v", err)
	}
	e.held()
	if e.sourceCount() != 0 {
		t.Fatalf("%d sources registered after the stop", e.sourceCount())
	}
	if err := e.c.ResumeHeld(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if err := e.ticks(2); err != nil {
		t.Fatal(err)
	}
	e.held() // resuming the run is not resuming the collaboration
	// Stopping the run itself stays available.
	if err := e.st.SetRunState(ctx, e.runID, "stopping", ""); err != nil {
		t.Fatalf("stopping the run at a stopped collaboration: %v", err)
	}
}

func TestAdmissionReservationReturnsWhenTheSendFailsForGood(t *testing.T) {
	e := newCEnv(t)
	e.tr.sendErr = func(runner.Delivery) error { return runner.NotSent(errors.New("the pane is gone")) }
	e.startEnforced(1000)
	for i := 0; i < 8 && e.tr.attempts < 3; i++ {
		e.c.Tick(ctx, e.runID)
		e.clk.advance(time.Minute)
	}
	if e.tr.attempts != 3 {
		t.Fatalf("%d send attempts, want the bounded 3", e.tr.attempts)
	}
	if b := e.codexBudget(); b.InputReserved != 0 || b.OutputReserved != 0 {
		t.Fatalf("a launch that certainly delivered nothing kept its reservation: %+v", b)
	}
	var admissions int
	if err := e.raw().QueryRow(`SELECT count(*) FROM dispatch_admissions WHERE state='released'`).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("%d released admissions, %v: one admission per effect across its retries", admissions, err)
	}
}

func TestAdmissionNotNeededWithoutEnforcement(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	e.tickUntilSent(1)
	if flight, _ := e.st.InFlightAdmissions(ctx, testCollab); len(flight) != 0 || e.sourceCount() != 1 {
		t.Fatalf("%d admissions and %d sources in a collaboration that does not enforce budgets", len(flight), e.sourceCount())
	}
}

func int64Ptr(v int64) *int64 { return &v }
