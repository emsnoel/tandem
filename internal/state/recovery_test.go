package state

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// recoveryEnv is two runs of one collaboration, each holding its checkout's
// writer lease, as an original run and its replacement.
type recoveryEnv struct {
	*collabEnv
	fenceA, fenceB int64
	n              int
}

func newRecoveryEnv(t *testing.T) *recoveryEnv {
	t.Helper()
	e := &recoveryEnv{collabEnv: newCollabEnv(t)}
	var err error
	if e.fenceA, err = e.s.AcquireLease(ctx, e.a.CheckoutID, "controller-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if e.fenceB, err = e.s.AcquireLease(ctx, e.b.CheckoutID, "controller-b", time.Hour); err != nil {
		t.Fatal(err)
	}
	return e
}

// request is the repair of run r's current attempt.
func (e *recoveryEnv) request(t *testing.T, r *seed) RecoveryRequest {
	t.Helper()
	e.n++
	run, err := e.s.GetRun(ctx, r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := e.s.GetAttempt(ctx, r.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := e.s.StopGeneration(ctx, e.collabID)
	if err != nil {
		t.Fatal(err)
	}
	fence := e.fenceA
	if r == e.b {
		fence = e.fenceB
	}
	current := r.artifact(t, "tree the failure left")
	return RecoveryRequest{ID: hexID(7000 + e.n), CollaborationID: e.collabID, RunID: r.RunID, TaskID: r.TaskID, ParentAttemptID: r.AttemptID,
		ParentVersion: parent.Version, Cause: CauseCheckFailure, EvidenceHash: r.artifact(t, "failing check log"),
		OriginalTreeHash: r.StartHash, CurrentTreeHash: current, RunVersion: run.Version, StopGeneration: gen, LeaseFence: fence,
		Attempt: AttemptSpec{ID: hexID(7100 + e.n), RunID: r.RunID, TaskID: r.TaskID, ParentID: r.AttemptID, Ordinal: 2,
			StartSnapshotHash: r.StartHash, PacketHash: r.artifact(t, "successor packet")}}
}

func (e *recoveryEnv) used(t *testing.T, group string) int {
	t.Helper()
	return e.count(t, `SELECT repairs_used FROM budget_groups WHERE id=?`, group)
}

func (e *recoveryEnv) bind(t *testing.T, work, alias string, r *seed) {
	t.Helper()
	if err := e.s.AttachWorkAlias(ctx, AliasRequest{CollaborationID: e.collabID, LogicalWorkID: work, Alias: alias, RunID: r.RunID, TaskID: r.TaskID}); err != nil {
		t.Fatal(err)
	}
}

func (e *recoveryEnv) current(t *testing.T, attempt string) bool {
	t.Helper()
	a, err := e.s.GetAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return a.Current
}

func TestRepairAdmissionIsOncePerRequestAndNeverTwicePerAttempt(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	req := e.request(t, e.a)
	first, err := e.s.AdmitRepair(ctx, req)
	if err != nil || first.Duplicate || first.SuccessorID != req.Attempt.ID {
		t.Fatalf("admission %+v %v", first, err)
	}
	if e.used(t, hexID(951)) != 1 || e.current(t, e.a.AttemptID) || !e.current(t, req.Attempt.ID) {
		t.Fatal("the allowance was not consumed, or the parent is still current, or the successor is not")
	}
	again, err := e.s.AdmitRepair(ctx, req)
	if err != nil || !again.Duplicate || again.SuccessorID != first.SuccessorID || len(again.Groups) != 1 {
		t.Fatalf("a retry of the same request: %+v %v", again, err)
	}
	if e.used(t, hexID(951)) != 1 {
		t.Fatal("a retry consumed the allowance again")
	}
	other := req
	other.ID, other.Attempt.ID = hexID(7900), hexID(7901)
	if _, err := e.s.AdmitRepair(ctx, other); !errors.Is(err, workflow.ErrConflict) && !errors.Is(err, ErrRepairExhausted) && !errors.Is(err, workflow.ErrStaleAttempt) {
		t.Fatalf("a second request for the same failed attempt: %v", err)
	}
	reused := req
	reused.Cause = CauseStuck
	if _, err := e.s.AdmitRepair(ctx, reused); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a request id reused for another cause: %v", err)
	}
	if got, ok, _ := e.s.RepairAdmissionFor(ctx, e.a.AttemptID); !ok || got.SuccessorID != req.Attempt.ID {
		t.Fatalf("admission for the parent: %+v %v", got, ok)
	}
}

func TestRepairAcrossReplacementRuns(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	e.bind(t, hexID(901), "task-1-replacement", e.b)
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.a)); err != nil {
		t.Fatal(err)
	}
	// The replacement run's task is the same logical work: its allowance is spent.
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.b)); !errors.Is(err, ErrRepairExhausted) {
		t.Fatalf("a repair in the replacement run: %v", err)
	}
	if !e.current(t, e.b.AttemptID) {
		t.Fatal("a refused repair superseded the replacement run's attempt")
	}
	if got := e.used(t, hexID(951)); got != 1 {
		t.Fatalf("group used %d times", got)
	}
}

func TestSplitCannotResetRepairBudget(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.a)); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SplitWork(ctx, SplitRequest{CollaborationID: e.collabID, ParentID: hexID(901),
		Children: []WorkChild{{ID: hexID(911), Alias: "task-1a"}, {ID: hexID(912), Alias: "task-1b"}}}); err != nil {
		t.Fatal(err)
	}
	e.bind(t, hexID(911), "task-1a-run-b", e.b)
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.b)); !errors.Is(err, ErrRepairExhausted) {
		t.Fatalf("a repair of a piece of split work: %v", err)
	}
	if e.count(t, `SELECT count(*) FROM budget_groups WHERE collaboration_id=?`, e.collabID) != 1 || e.used(t, hexID(951)) != 1 {
		t.Fatal("the split minted a group or reset a count")
	}
}

func TestMergeConsumesAllOrigins(t *testing.T) {
	e := newRecoveryEnv(t)
	e.work(t, hexID(901), hexID(951), "one")
	e.work(t, hexID(902), hexID(952), "two")
	if err := e.s.MergeWork(ctx, MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(901), hexID(902)}, ID: hexID(921), Alias: "merged"}); err != nil {
		t.Fatal(err)
	}
	e.bind(t, hexID(921), "merged-a", e.a)
	e.bind(t, hexID(921), "merged-b", e.b)
	adm, err := e.s.AdmitRepair(ctx, e.request(t, e.a))
	if err != nil || len(adm.Groups) != 2 {
		t.Fatalf("a merged work's repair: %+v %v", adm, err)
	}
	if e.used(t, hexID(951)) != 1 || e.used(t, hexID(952)) != 1 {
		t.Fatal("not every origin was consumed")
	}
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.b)); !errors.Is(err, ErrRepairExhausted) {
		t.Fatalf("a second repair of merged work: %v", err)
	}
}

// Acquiring one origin's allowance and failing on another takes nothing.
func TestMergeAcquiresAllOriginsOrNone(t *testing.T) {
	e := newRecoveryEnv(t)
	e.work(t, hexID(901), hexID(951), "one")
	e.work(t, hexID(902), hexID(952), "two")
	e.bind(t, hexID(902), "two-b", e.b)
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.b)); err != nil { // origin two is spent
		t.Fatal(err)
	}
	if err := e.s.MergeWork(ctx, MergeRequest{CollaborationID: e.collabID, SourceIDs: []string{hexID(901), hexID(902)}, ID: hexID(921), Alias: "merged"}); err != nil {
		t.Fatal(err)
	}
	e.bind(t, hexID(921), "merged-a", e.a)
	if _, err := e.s.AdmitRepair(ctx, e.request(t, e.a)); !errors.Is(err, ErrRepairExhausted) {
		t.Fatalf("a merge with one spent origin: %v", err)
	}
	if e.used(t, hexID(951)) != 0 {
		t.Fatal("the unspent origin was consumed although the repair was refused")
	}
	if !e.current(t, e.a.AttemptID) {
		t.Fatal("a refused repair superseded the attempt")
	}
}

func TestRepairRefusals(t *testing.T) {
	cases := map[string]struct {
		setup func(*testing.T, *recoveryEnv, *RecoveryRequest)
		want  error
	}{
		"a task bound to no work": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {}, ErrWorkMapping},
		"work split away from the task": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			if err := e.s.SplitWork(ctx, SplitRequest{CollaborationID: e.collabID, ParentID: hexID(901),
				Children: []WorkChild{{ID: hexID(911), Alias: "a"}, {ID: hexID(912), Alias: "b"}}}); err != nil {
				t.Fatal(err)
			}
		}, ErrWorkMapping},
		"a stopped collaboration": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			if err := e.s.StopCollaboration(ctx, ControlRequest{CollaborationID: e.collabID, Actor: "user", Reason: "hold"}); err != nil {
				t.Fatal(err)
			}
		}, ErrStopped},
		"a stop after the request was made": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			r.StopGeneration--
		}, ErrStopped},
		"a run paused for another reason": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			e.a.exec(t, `UPDATE runs SET state='paused', pause_code='source_mutated' WHERE id=?`, e.a.RunID)
		}, workflow.ErrInvalidTransition},
		"a moved run version": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			r.RunVersion++
		}, workflow.ErrConflict},
		"a lost writer lease": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			r.LeaseFence++
		}, workflow.ErrBusy},
		"a stale parent version": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			r.ParentVersion++
		}, workflow.ErrStaleAttempt},
		"an accepted task": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			e.a.exec(t, `UPDATE tasks SET accepted_attempt_id=? WHERE run_id=? AND id=?`, e.a.AttemptID, e.a.RunID, e.a.TaskID)
		}, workflow.ErrConflict},
		"a successor id that already exists": {func(t *testing.T, e *recoveryEnv, r *RecoveryRequest) {
			e.boundWork(t, hexID(901), hexID(951), "task-1")
			r.Attempt.ID = e.b.AttemptID
		}, workflow.ErrConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newRecoveryEnv(t)
			req := e.request(t, e.a)
			tc.setup(t, e, &req)
			if name == "a stopped collaboration" {
				req.StopGeneration, _ = e.s.StopGeneration(ctx, e.collabID) // current, but admission is closed
			}
			if _, err := e.s.AdmitRepair(ctx, req); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if n := e.count(t, `SELECT count(*) FROM repair_admissions`); n != 0 {
				t.Fatalf("a refused repair left %d admissions", n)
			}
			if n := e.count(t, `SELECT coalesce(sum(repairs_used),0) FROM budget_groups`); n != 0 {
				t.Fatalf("a refused repair consumed %d allowances", n)
			}
			if !e.current(t, e.a.AttemptID) && name != "an accepted task" {
				t.Fatal("a refused repair superseded the failed attempt")
			}
		})
	}
}

// A run paused by the failure itself may be repaired; the repair does not need
// the run to be going on.
func TestStuckRunPausedByTheFailureAdmitsItsRepair(t *testing.T) {
	for _, code := range []string{workflow.PauseStuck, workflow.PauseCheckFailed, workflow.PauseReviewFailed} {
		e := newRecoveryEnv(t)
		e.boundWork(t, hexID(901), hexID(951), "task-1")
		e.a.exec(t, `UPDATE runs SET state='paused', pause_code=? WHERE id=?`, code, e.a.RunID)
		if _, err := e.s.AdmitRepair(ctx, e.request(t, e.a)); err != nil {
			t.Errorf("a run paused as %s: %v", code, err)
		}
	}
}

func TestRepairSuccessorMustStartFromTheOriginalTree(t *testing.T) {
	e := newRecoveryEnv(t)
	e.boundWork(t, hexID(901), hexID(951), "task-1")
	req := e.request(t, e.a)
	req.Attempt.StartSnapshotHash = req.CurrentTreeHash
	if _, err := e.s.AdmitRepair(ctx, req); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a successor that starts from the failure's tree: %v", err)
	}
}
