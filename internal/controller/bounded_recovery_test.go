package controller

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	recoveryWork  = "0000000000000000000000000000d001"
	recoveryGroup = "0000000000000000000000000000e001"
)

// bindRecoveryWork makes the run's task the logical work whose single origin
// budget group holds the automatic repair.
func (e *cenv) bindRecoveryWork(taskID string) {
	e.t.Helper()
	if err := e.st.CreateLogicalWork(ctx, state.WorkRequest{CollaborationID: testCollab, ID: recoveryWork, BudgetGroupID: recoveryGroup,
		Alias: "recovery-" + taskID, RunID: e.runID, TaskID: taskID}); err != nil {
		e.t.Fatal(err)
	}
}

// stuckAttempt takes task-1's first attempt through a start, some work and a
// stuck report, and settles its builder. The run pauses as stuck.
func (e *cenv) stuckAttempt() workflow.Attempt {
	e.t.Helper()
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	write(e.t, e.root, "work1.txt", "first attempt, half done\n")
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderStuck, "{\"why\":\"the parser needs a design call\"}\n")
	e.tick()
	if st, code := e.runState(); st != "paused" || code != workflow.PauseStuck {
		e.t.Fatalf("run %s/%s after a stuck report", st, code)
	}
	cur, err := e.st.GetAttempt(ctx, att.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return cur
}

func (e *cenv) successorOf(parent workflow.Attempt, cause string) (workflow.Attempt, error) {
	e.t.Helper()
	run, cfg, err := e.c.load(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.c.admitSuccessor(ctx, run, cfg, parent, cause)
}

func (e *cenv) usedRepairs() int {
	e.t.Helper()
	return e.countWhere(`SELECT coalesce(sum(repairs_used),0) FROM budget_groups`)
}

// The successor starts from the task's original tree. Its final patch is the
// whole task: the stuck attempt's retained edit, a manual change made between
// the attempts, and the successor's own work.
func TestRepairWholeTaskDiff(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	e.bindRecoveryWork("task-1")
	parent := e.stuckAttempt()
	write(t, e.root, "manual.txt", "a person fixed this by hand\n")

	succ, err := e.successorOf(parent, state.CauseStuck)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"failure_evidence", "task_baseline", "failed_tree"} {
		if e.countWhere(`SELECT count(*) FROM artifact_refs WHERE owner_kind='attempt' AND owner_id=? AND role=?`, succ.ID, role) != 1 {
			t.Errorf("the successor cannot read its %s through the artifact protocol", role)
		}
	}
	if succ.Ordinal != parent.Ordinal+1 || succ.ParentAttemptID != parent.ID || succ.StartSnapshotHash != parent.StartSnapshotHash {
		t.Fatalf("successor %+v: it must be the next attempt, linked, and start from the task's original tree", succ)
	}
	if st, code := e.runState(); st != "running" {
		t.Fatalf("the run is %s/%s after the repair was admitted", st, code)
	}
	prompt := e.promptText("builder_dispatch", succ)
	for _, want := range []string{"the one automatic recovery of attempt 1", "work1.txt", "manual.txt", "the parser needs a design call", "tandem artifact read"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the successor's prompt lacks %q:\n%s", want, prompt)
		}
	}

	// The successor finishes the task; checks and review then see everything.
	e.tick()
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	write(t, e.root, "work2.txt", "the rest of the task\n")
	e.settle(succ)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	cur, _ := e.st.GetAttempt(ctx, succ.ID)
	paths, err := e.c.changedPaths(ctx, cur.PatchHash)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths, ","); got != "manual.txt,work1.txt,work2.txt" {
		t.Fatalf("the repaired attempt's whole-task diff is %q: it must keep the failed attempt's edit and the manual change", got)
	}
	if cur.Check != workflow.CheckPassed {
		t.Fatalf("the declared check did not run on the repaired evidence: %s", cur.Check)
	}
	if err := e.review(succ, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.accepted() != 1 {
		t.Fatal("the repaired task was not accepted after its checks and review")
	}
}

// A stuck attempt is not given a submitted snapshot, and its late results
// cannot accept the successor.
func TestStuckAttemptKeepsNoFabricatedEvidenceAndCannotAcceptTheSuccessor(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	e.bindRecoveryWork("task-1")
	parent := e.stuckAttempt()
	succ, err := e.successorOf(parent, state.CauseStuck)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := e.st.GetAttempt(ctx, parent.ID)
	if old.EndSnapshotHash != "" || old.PatchHash != "" || old.Current || old.Implementation != workflow.ImplementationStuck {
		t.Fatalf("the stuck attempt after its repair: %+v", old)
	}
	err = e.sendFor(parent, "task-1", workflow.EventBuilderSubmitted, "{\"message\":\"late\"}\n")
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a late submission from the replaced attempt: %v", err)
	}
	if e.accepted() != 0 {
		t.Fatal("a stale result accepted the task")
	}
	if cur := e.current("task-1"); cur.ID != succ.ID {
		t.Fatalf("current attempt %s, want the successor", cur.ID)
	}
}

// Asking again, or asking after a restart, finds the same successor and
// consumes nothing more.
func TestRepairIsOnceAcrossRetriesAndRestart(t *testing.T) {
	e := ownedEnv(t)
	e.startInCollaboration()
	e.bindRecoveryWork("task-1")
	parent := e.stuckAttempt()
	first, err := e.successorOf(parent, state.CauseStuck)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		e.restart()
		again, err := e.successorOf(parent, state.CauseStuck)
		if err != nil || again.ID != first.ID {
			t.Fatalf("retry %d: %+v %v", i, again, err)
		}
	}
	if e.usedRepairs() != 1 {
		t.Fatalf("the allowance was consumed %d times", e.usedRepairs())
	}
	for i := 0; i < 3; i++ {
		e.tick()
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 2 {
		t.Fatalf("%d builder prompts: one for the first attempt and one for the successor", got)
	}
	// A second failure of the successor finds the allowance spent.
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.settle(first)
	e.mustSend("task-1", workflow.EventBuilderStuck, "{\"why\":\"still stuck\"}\n")
	e.tick()
	second, _ := e.st.GetAttempt(ctx, first.ID)
	if _, err := e.successorOf(second, state.CauseStuck); !errors.Is(err, state.ErrRepairExhausted) {
		t.Fatalf("a second automatic repair: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != pauseRecovery {
		t.Fatalf("run %s/%s after an exhausted allowance", st, code)
	}
}

func TestRepairRefusals(t *testing.T) {
	t.Run("a builder that may still be writing", func(t *testing.T) {
		e := newCEnv(t)
		e.startInCollaboration()
		e.bindRecoveryWork("task-1")
		parent := e.stuckAttempt()
		e.tr.set(e.builderEffect(parent).ID, runner.Observation{State: runner.ObsRunning, Nonce: e.builderEffect(parent).Nonce})
		if _, err := e.successorOf(parent, state.CauseStuck); !errors.Is(err, workflow.ErrBusy) {
			t.Fatalf("got %v", err)
		}
		if e.usedRepairs() != 0 || !e.current("task-1").Current {
			t.Fatal("a refused repair consumed the allowance or superseded the attempt")
		}
	})
	t.Run("a cause the attempt does not show", func(t *testing.T) {
		e := newCEnv(t)
		e.startInCollaboration()
		e.bindRecoveryWork("task-1")
		parent := e.stuckAttempt()
		for _, cause := range []string{state.CauseReviewChanges, state.CauseCheckFailure, "guess"} {
			if _, err := e.successorOf(parent, cause); err == nil {
				t.Errorf("cause %q was accepted for a stuck attempt", cause)
			}
		}
		if e.usedRepairs() != 0 {
			t.Fatal("a refused repair consumed the allowance")
		}
	})
	t.Run("a stuck cause claimed for an attempt that is not stuck", func(t *testing.T) {
		e := newCEnv(t)
		e.startInCollaboration()
		e.bindRecoveryWork("task-1")
		e.tick()
		att := e.current("task-1")
		e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
		e.settle(att)
		cur, _ := e.st.GetAttempt(ctx, att.ID)
		if _, err := e.successorOf(cur, state.CauseStuck); !errors.Is(err, workflow.ErrIncompleteEvidence) {
			t.Fatalf("got %v", err)
		}
		if e.usedRepairs() != 0 {
			t.Fatal("an unknown classification consumed the allowance")
		}
	})
	t.Run("a task bound to no logical work pauses the run", func(t *testing.T) {
		e := newCEnv(t)
		e.startInCollaboration()
		parent := e.stuckAttempt()
		if _, err := e.successorOf(parent, state.CauseStuck); !errors.Is(err, state.ErrWorkMapping) {
			t.Fatalf("got %v", err)
		}
		if st, code := e.runState(); st != "paused" || code != pauseRecovery {
			t.Fatalf("run %s/%s: an unmapped repair waits for a person", st, code)
		}
	})
	t.Run("a run outside any collaboration", func(t *testing.T) {
		e := newCEnv(t)
		e.start()
		e.tick()
		att := e.current("task-1")
		if _, err := e.successorOf(att, state.CauseStuck); !errors.Is(err, workflow.ErrUnsupported) {
			t.Fatalf("got %v", err)
		}
	})
}

// A stop placed after the successor exists still prevents its dispatch.
func TestRepairStopAfterAdmissionPreventsNewWork(t *testing.T) {
	e := newCEnv(t)
	e.startEnforced(100000)
	e.tickUntilSent(1)
	if err := e.st.CreateLogicalWork(ctx, state.WorkRequest{CollaborationID: enforcedCollab, ID: recoveryWork, BudgetGroupID: recoveryGroup,
		Alias: "enforced-task-1", RunID: e.runID, TaskID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	write(t, e.root, "work1.txt", "partial\n")
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderStuck, "{\"why\":\"cannot\"}\n")
	e.tick()
	parent, _ := e.st.GetAttempt(ctx, att.ID)
	succ, err := e.successorOf(parent, state.CauseStuck)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.StopCollaboration(ctx, state.ControlRequest{CollaborationID: enforcedCollab, Actor: "user", Reason: "hold"}); err != nil {
		t.Fatal(err)
	}
	before := e.delivered()[workflow.EffectBuilderDispatch]
	if err := e.ticks(4); err != nil {
		t.Fatal(err)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != before {
		t.Fatalf("the successor %s was dispatched after a stop (%d prompts)", succ.ID, got)
	}
	if e.usedRepairs() != 1 {
		t.Fatalf("the allowance counts %d: a stop neither refunds nor repeats it", e.usedRepairs())
	}
	_ = os.Remove(filepath.Join(e.root, "work1.txt"))
}
