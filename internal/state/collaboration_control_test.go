package state

import (
	"errors"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func (e *admitEnv) stop(t *testing.T) {
	t.Helper()
	if err := e.s.StopCollaboration(ctx, ControlRequest{CollaborationID: e.enforced, Actor: "user", Reason: "stop now"}); err != nil {
		t.Fatal(err)
	}
}

func TestStopFirstForbidsAdmission(t *testing.T) {
	e := newAdmitEnv(t, 1000)
	before := e.request(t, 5001, 10, 1)
	after := e.request(t, 5002, 10, 1) // its source is registered before the stop
	e.stop(t)
	if _, err := e.s.AdmitDispatch(ctx, before); !errors.Is(err, ErrStopped) {
		t.Fatalf("an admission computed before the stop: %v", err)
	}
	// Reading the new generation is not a resume.
	gen, _ := e.s.StopGeneration(ctx, e.enforced)
	if gen != before.StopGeneration+1 {
		t.Fatalf("generation %d -> %d", before.StopGeneration, gen)
	}
	after.StopGeneration = gen
	if _, err := e.s.AdmitDispatch(ctx, after); !errors.Is(err, ErrStopped) {
		t.Fatalf("an admission at the new generation without a resume: %v", err)
	}
	if err := e.s.RegisterSource(ctx, SourceRequest{ID: hexID(5099), CollaborationID: e.enforced, Provider: "codex", Role: "builder"}); !errors.Is(err, ErrStopped) {
		t.Fatalf("a launch registered while stopped: %v", err)
	}
	if err := e.s.ResumeCollaboration(ctx, ControlRequest{CollaborationID: e.enforced}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a resume without actor and reason: %v", err)
	}
	if err := e.s.ResumeCollaboration(ctx, ControlRequest{CollaborationID: e.enforced, Actor: "user", Reason: "carry on"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AdmitDispatch(ctx, e.request(t, 5003, 10, 1)); err != nil {
		t.Fatalf("after an audited resume: %v", err)
	}
	var stops, resumes int
	e.s.db.QueryRow(`SELECT count(*) FROM collaboration_audit WHERE action='stop'`).Scan(&stops)
	e.s.db.QueryRow(`SELECT count(*) FROM collaboration_audit WHERE action='resume' AND actor='user' AND reason='carry on'`).Scan(&resumes)
	if stops != 1 || resumes != 1 {
		t.Fatalf("%d stop and %d resume audit rows", stops, resumes)
	}
}

// The admission's transaction is held open at its commit boundary while a
// stop is attempted on another connection. The stop waits; the admission
// commits at the old generation and stays in flight after the stop.
func TestStopAfterAdmissionLeavesItInFlight(t *testing.T) {
	e := newAdmitEnv(t, 1000)
	req := e.request(t, 5101, 10, 1)
	later := e.request(t, 5102, 10, 1)
	other, err := Open(ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	e.s.admitHooks.beforeCommit = func() { close(entered); <-release }
	admitted := make(chan error, 1)
	go func() {
		_, err := e.s.AdmitDispatch(ctx, req)
		admitted <- err
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() {
		stopped <- other.StopCollaboration(ctx, ControlRequest{CollaborationID: e.enforced, Actor: "user", Reason: "stop"})
	}()
	close(release)
	if err := <-admitted; err != nil {
		t.Fatalf("the admission that reached its commit first: %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("the stop: %v", err)
	}
	e.s.admitHooks.beforeCommit = nil
	flight, err := e.s.InFlightAdmissions(ctx, e.enforced)
	if err != nil || len(flight) != 1 || flight[0].ID != req.ID || flight[0].StopGeneration != req.StopGeneration {
		t.Fatalf("in flight %+v, %v", flight, err)
	}
	if gen, _ := e.s.StopGeneration(ctx, e.enforced); gen != req.StopGeneration+1 {
		t.Fatalf("the stop did not advance the generation: %d", gen)
	}
	// The admitted effect may still be sent; nothing new is admitted.
	later.StopGeneration = req.StopGeneration + 1
	if _, err := e.s.AdmitDispatch(ctx, later); !errors.Is(err, ErrStopped) {
		t.Fatalf("a new admission after the stop: %v", err)
	}
}

func TestStopRemainsAvailableAtTheCeiling(t *testing.T) {
	e := newAdmitEnv(t, 30)
	if _, err := e.s.AdmitDispatch(ctx, e.request(t, 5201, 30, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AdmitDispatch(ctx, e.request(t, 5202, 1, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("over the ceiling: %v", err)
	}
	e.stop(t)
	e.stop(t) // stopping a stopped collaboration is a no-op
	if gen, _ := e.s.StopGeneration(ctx, e.enforced); gen != 1 {
		t.Fatalf("generation %d after two stops", gen)
	}
	if _, err := e.s.db.Exec(`UPDATE collaborations SET status='closing' WHERE id=?`, e.enforced); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ResumeCollaboration(ctx, ControlRequest{CollaborationID: e.enforced, Actor: "user", Reason: "reopen"}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a closing collaboration resumed: %v", err)
	}
	if err := e.s.StopCollaboration(ctx, ControlRequest{CollaborationID: e.enforced, Actor: "user", Reason: "again"}); err != nil {
		t.Fatalf("stopping a closing collaboration: %v", err)
	}
}
