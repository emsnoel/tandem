package state

import (
	"errors"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func (e *recoveryEnv) incident(t *testing.T, key int) IncidentSpec {
	t.Helper()
	return IncidentSpec{Key: hexID(key), CollaborationID: e.collabID, RunID: e.a.RunID, TaskID: e.a.TaskID, AttemptID: e.a.AttemptID,
		Kind: "unknown", Reason: "no automatic rule covers this failure", EvidenceHash: e.a.artifact(t, "evidence")}
}

func TestIncidentIsOpenedOnceAndNotifiedOnce(t *testing.T) {
	e := newRecoveryEnv(t)
	spec := e.incident(t, 8001)
	first, created, err := e.s.OpenIncident(ctx, spec)
	if err != nil || !created || first.State != "open" {
		t.Fatalf("%+v %v %v", first, created, err)
	}
	again, created, err := e.s.OpenIncident(ctx, spec)
	if err != nil || created || again.Key != first.Key {
		t.Fatalf("a duplicate failure opened another incident: %+v %v %v", again, created, err)
	}
	if err := e.s.NoteIncidentNotified(ctx, spec.Key, hexID(8101)); err != nil {
		t.Fatal(err)
	}
	if err := e.s.NoteIncidentNotified(ctx, spec.Key, hexID(8101)); err != nil {
		t.Fatalf("noting the same request again: %v", err)
	}
	if err := e.s.NoteIncidentNotified(ctx, spec.Key, hexID(8102)); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a second question for one incident: %v", err)
	}
	// Opening it again after it was notified changes nothing.
	if got, created, _ := e.s.OpenIncident(ctx, spec); created || got.State != "notified" || got.DecisionEffectID != hexID(8101) {
		t.Fatalf("%+v %v", got, created)
	}
	if err := e.s.ResolveIncident(ctx, spec.Key, "decided"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ResolveIncident(ctx, spec.Key, "decided"); err != nil {
		t.Fatalf("resolving twice the same way: %v", err)
	}
	if err := e.s.ResolveIncident(ctx, spec.Key, "something else"); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("resolving an incident two ways: %v", err)
	}
	if err := e.s.ResolveIncident(ctx, spec.Key, ""); err == nil {
		t.Fatal("resolved with no reason")
	}
	bad := spec
	bad.Key = "short"
	if _, _, err := e.s.OpenIncident(ctx, bad); err == nil {
		t.Fatal("an incident with a malformed key")
	}
	ghost := e.incident(t, 8002)
	ghost.AttemptID = hexID(8999)
	if _, _, err := e.s.OpenIncident(ctx, ghost); err == nil {
		t.Fatal("an incident for an attempt that does not exist")
	}
}

func (e *recoveryEnv) reconcileRequest(t *testing.T) ReconcileRequest {
	t.Helper()
	e.n++
	run, _ := e.s.GetRun(ctx, e.a.RunID)
	parent, _ := e.s.GetAttempt(ctx, e.a.AttemptID)
	return ReconcileRequest{ID: hexID(8200 + e.n), RunID: e.a.RunID, TaskID: e.a.TaskID, FailedAttemptID: e.a.AttemptID, FailedVersion: parent.Version,
		Actor: "user", ReasonHash: e.a.artifact(t, "I fixed the parser by hand"), TreeHash: e.a.artifact(t, "tree with the edit"),
		RunVersion: run.Version, LeaseFence: e.fenceA,
		Attempt: AttemptSpec{ID: hexID(8300 + e.n), RunID: e.a.RunID, TaskID: e.a.TaskID, ParentID: e.a.AttemptID, Ordinal: 2,
			StartSnapshotHash: e.a.StartHash, PacketHash: e.a.artifact(t, "reconcile packet")}}
}

func (e *recoveryEnv) pause(t *testing.T, code string) {
	t.Helper()
	e.a.exec(t, `UPDATE runs SET state='paused', pause_code=? WHERE id=?`, code, e.a.RunID)
}

func TestReconcileSupersedesOnceAndTouchesNoAllowance(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	e.pause(t, workflow.PauseCheckFailed)
	if _, _, err := e.s.OpenIncident(ctx, e.incident(t, 8001)); err != nil {
		t.Fatal(err)
	}
	req := e.reconcileRequest(t)
	first, err := e.s.ReconcileAttempt(ctx, req)
	if err != nil || first.Duplicate || first.AttemptID != req.Attempt.ID {
		t.Fatalf("%+v %v", first, err)
	}
	if e.used(t, hexID(951)) != 0 {
		t.Fatal("a reconcile consumed an automatic repair")
	}
	if e.current(t, e.a.AttemptID) || !e.current(t, req.Attempt.ID) {
		t.Fatal("the failed attempt is still current, or the reconcile attempt is not")
	}
	if n, _ := e.s.ManualInterventions(ctx, e.a.RunID); n != 1 {
		t.Fatalf("%d interventions", n)
	}
	if inc, _, _ := e.s.OpenIncident(ctx, e.incident(t, 8001)); inc.State != "resolved" || inc.Resolution != "reconciled" {
		t.Fatalf("the incident about the failure: %+v", inc)
	}
	again, err := e.s.ReconcileAttempt(ctx, req)
	if err != nil || !again.Duplicate || again.AttemptID != first.AttemptID {
		t.Fatalf("a retry: %+v %v", again, err)
	}
	if n, _ := e.s.ManualInterventions(ctx, e.a.RunID); n != 1 {
		t.Fatalf("a retry counted %d interventions", n)
	}
}

// A reconcile of the successor, after the automatic repair was spent, neither
// resets nor consumes anything: the allowance stays used.
func TestReconcileCannotResetASpentAllowance(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	adm, err := e.s.AdmitRepair(ctx, e.request(t, e.a))
	if err != nil {
		t.Fatal(err)
	}
	e.pause(t, workflow.PauseCheckFailed)
	run, _ := e.s.GetRun(ctx, e.a.RunID)
	succ, _ := e.s.GetAttempt(ctx, adm.SuccessorID)
	e.n++
	req := ReconcileRequest{ID: hexID(8500 + e.n), RunID: e.a.RunID, TaskID: e.a.TaskID, FailedAttemptID: succ.ID, FailedVersion: succ.Version,
		Actor: "user", ReasonHash: e.a.artifact(t, "hand fix"), TreeHash: e.a.artifact(t, "tree"), RunVersion: run.Version, LeaseFence: e.fenceA,
		Attempt: AttemptSpec{ID: hexID(8600 + e.n), RunID: e.a.RunID, TaskID: e.a.TaskID, ParentID: succ.ID, Ordinal: succ.Ordinal + 1,
			StartSnapshotHash: e.a.StartHash, PacketHash: e.a.artifact(t, "reconcile packet 2")}}
	if _, err := e.s.ReconcileAttempt(ctx, req); err != nil {
		t.Fatal(err)
	}
	if e.used(t, hexID(951)) != 1 {
		t.Fatalf("the allowance reads %d after a reconcile", e.used(t, hexID(951)))
	}
	// And the allowance still cannot be used again.
	if n := e.count(t, `SELECT count(*) FROM repair_admissions`); n != 1 {
		t.Fatalf("%d automatic admissions", n)
	}
}

func TestReconcileRefusals(t *testing.T) {
	cases := map[string]struct {
		setup func(*testing.T, *recoveryEnv, *ReconcileRequest)
		want  error
	}{
		"a run that is not paused by a failure": {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) {}, workflow.ErrInvalidTransition},
		"a run paused for another reason":       {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) { e.pause(t, "source_mutated") }, workflow.ErrInvalidTransition},
		"a moved run version": {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) {
			e.pause(t, workflow.PauseStuck)
			r.RunVersion++
		}, workflow.ErrConflict},
		"a lost lease": {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) {
			e.pause(t, workflow.PauseStuck)
			r.LeaseFence++
		}, workflow.ErrBusy},
		"a stale failed attempt": {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) {
			e.pause(t, workflow.PauseStuck)
			r.FailedVersion++
		}, workflow.ErrStaleAttempt},
		"an accepted task": {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) {
			e.pause(t, workflow.PauseStuck)
			e.a.exec(t, `UPDATE tasks SET accepted_attempt_id=? WHERE run_id=? AND id=?`, e.a.AttemptID, e.a.RunID, e.a.TaskID)
		}, workflow.ErrConflict},
		"every attempt used": {func(t *testing.T, e *recoveryEnv, r *ReconcileRequest) {
			e.pause(t, workflow.PauseStuck)
			r.Attempt.Ordinal = MaxAttempts + 1
		}, workflow.ErrConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newRecoveryEnv(t)
			req := e.reconcileRequest(t)
			tc.setup(t, e, &req)
			if _, err := e.s.ReconcileAttempt(ctx, req); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if n, _ := e.s.ManualInterventions(ctx, e.a.RunID); n != 0 {
				t.Fatalf("a refused reconcile left %d interventions", n)
			}
			if name != "an accepted task" && !e.current(t, e.a.AttemptID) {
				t.Fatal("a refused reconcile superseded the failed attempt")
			}
		})
	}
}

func TestWorkRepairBudgetsAndTaskWork(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	b, err := e.s.WorkRepairBudgets(ctx, e.collabID, hexID(901))
	if err != nil || len(b) != 1 || b[0].Allowance != 1 || b[0].Used != 0 {
		t.Fatalf("%+v %v", b, err)
	}
	if w, _ := e.s.TaskWork(ctx, e.a.RunID, e.a.TaskID); w != hexID(901) {
		t.Fatalf("task work %q", w)
	}
	if w, _ := e.s.TaskWork(ctx, e.b.RunID, e.b.TaskID); w != "" {
		t.Fatalf("an unbound task has work %q", w)
	}
}
