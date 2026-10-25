package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

var openFinding = workflow.Finding{Severity: workflow.SeverityHigh, Path: "work1.txt", Disposition: workflow.DispositionOpen,
	Message: "the greeting is deleted"}

// changesRequested drives task-1's current attempt to a review that asks for
// changes and ticks until the controller has reacted.
func (e *cenv) changesRequested(n int, findings ...workflow.Finding) workflow.Attempt {
	e.t.Helper()
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	write(e.t, e.root, "work1.txt", fmt.Sprintf("attempt %d\n", n))
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusChangesRequested, findings...); err != nil {
		e.t.Fatal(err)
	}
	e.tick()
	e.tick()
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	return cur
}

func (e *cenv) decisions() []workflow.Effect { return e.effects(effectRecoveryDecision) }

func (e *cenv) openIncidents() []state.Incident {
	e.t.Helper()
	all, err := e.st.RunIncidents(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	return all
}

// A reviewer's request for changes is repaired automatically once. When the
// successor needs changes too, the allowance is spent: the run stops, the
// planner is asked exactly once, and nothing replans.
func TestRecoveryReviewChangesRepairsOnceThenEscalatesOnce(t *testing.T) {
	e := ownedEnv(t)
	e.startRecovery(true)
	first := e.changesRequested(1, openFinding)
	if e.attemptsOf("task-1") != 2 || e.usedRepairs() != 1 {
		t.Fatalf("after one requested change: %d attempts, %d repairs used", e.attemptsOf("task-1"), e.usedRepairs())
	}
	succ := e.current("task-1")
	if succ.ParentAttemptID != first.ID || succ.StartSnapshotHash != first.StartSnapshotHash {
		t.Fatalf("successor %+v", succ)
	}
	if len(e.openIncidents()) != 0 || len(e.decisions()) != 0 {
		t.Fatal("a repaired failure raised an incident")
	}

	e.changesRequested(2, openFinding)
	if st, code := e.runState(); st != "paused" || code != pauseRecovery {
		t.Fatalf("run %s/%s after the allowance was spent", st, code)
	}
	if e.attemptsOf("task-1") != 2 {
		t.Fatal("a second automatic repair, or a replanned attempt, was made")
	}
	inc := e.openIncidents()
	if len(inc) != 1 || inc[0].Kind != kindReviewChanges || inc[0].State != "notified" || !strings.Contains(inc[0].Reason, "no automatic repair left") {
		t.Fatalf("incidents %+v", inc)
	}
	for i := 0; i < 5; i++ {
		e.restart()
		e.tick()
	}
	if got := e.delivered()[effectRecoveryDecision]; got != 1 || len(e.decisions()) != 1 {
		t.Fatalf("%d decision requests delivered, %d queued: one incident is one question", got, len(e.decisions()))
	}
	failed, _ := e.st.GetAttempt(ctx, succ.ID)
	text := e.promptText(effectRecoveryDecision, failed)
	for _, want := range []string{"A decision is needed", "1 of 1 used", "Evidence (artifact", "Provider budgets", "tandem build reconcile", "Nothing was replanned and no budget was raised", inc[0].Key} {
		if !strings.Contains(text, want) {
			t.Errorf("the decision request lacks %q:\n%s", want, text)
		}
	}
}

func TestRecoveryCheckFailureRepairsOnce(t *testing.T) {
	e := newCEnv(t)
	e.startRecovery(true)
	e.chk.exit = 1
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	write(t, e.root, "work1.txt", "broken\n")
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{}\n")
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if e.attemptsOf("task-1") != 2 || e.usedRepairs() != 1 {
		t.Fatalf("%d attempts, %d repairs used after a failed declared check", e.attemptsOf("task-1"), e.usedRepairs())
	}
	if st, _ := e.runState(); st != "running" {
		t.Fatalf("run %s: a repaired check failure carries on", st)
	}
	if len(e.openIncidents()) != 0 {
		t.Fatal("a repaired failure raised an incident")
	}
}

// Failures the policy does not repair automatically each become one incident
// and no attempt: a stuck builder, a review with nothing concrete to fix, and a
// task bound to no work.
func TestRecoveryIneligibleFailuresEscalateWithoutRepair(t *testing.T) {
	t.Run("a stuck builder", func(t *testing.T) {
		e := newCEnv(t)
		e.startRecovery(true)
		parent := e.stuckAttempt()
		for i := 0; i < 3; i++ {
			e.tick()
		}
		inc := e.openIncidents()
		if len(inc) != 1 || inc[0].Kind != kindStuck || inc[0].AttemptID != parent.ID {
			t.Fatalf("incidents %+v", inc)
		}
		if e.attemptsOf("task-1") != 1 || e.usedRepairs() != 0 {
			t.Fatal("a stuck attempt was repaired automatically")
		}
		if st, code := e.runState(); st != "paused" || code != workflow.PauseStuck {
			t.Fatalf("run %s/%s", st, code)
		}
	})
	t.Run("a review with nothing concrete to fix", func(t *testing.T) {
		e := newCEnv(t)
		e.startRecovery(true)
		vague := workflow.Finding{Severity: workflow.SeverityLow, Path: "work1.txt", Disposition: workflow.DispositionFixed, Message: "feels off, already handled"}
		e.changesRequested(1, vague)
		inc := e.openIncidents()
		if len(inc) != 1 || inc[0].Kind != kindReviewChanges || !strings.Contains(inc[0].Reason, "no open finding with a file") {
			t.Fatalf("incidents %+v", inc)
		}
		if e.attemptsOf("task-1") != 1 || e.usedRepairs() != 0 {
			t.Fatal("a failure with no concrete target was repaired automatically")
		}
	})
	t.Run("a task bound to no work", func(t *testing.T) {
		e := newCEnv(t)
		e.startRecovery(false)
		e.changesRequested(1, openFinding)
		inc := e.openIncidents()
		if len(inc) != 1 || !strings.Contains(inc[0].Reason, "bound to no logical work") {
			t.Fatalf("incidents %+v", inc)
		}
		if e.attemptsOf("task-1") != 1 {
			t.Fatal("a repair was made for work with no allowance")
		}
		text := e.promptText(effectRecoveryDecision, e.current("task-1"))
		if !strings.Contains(text, "bound to no logical work") {
			t.Errorf("the request does not say the task has no allowance:\n%s", text)
		}
	})
}

// A duplicate failure event, a replayed message and a restart open no second
// incident and send the planner nothing more.
func TestIncidentDuplicateFailureEventsAndRestartsAskOnce(t *testing.T) {
	e := ownedEnv(t)
	e.startRecovery(true)
	parent := e.stuckAttempt()
	e.tick()
	if len(e.decisions()) != 1 {
		t.Fatalf("%d decision requests queued", len(e.decisions()))
	}
	// The builder's stuck message arrives again (archived traffic replayed).
	for i := 0; i < 3; i++ {
		_ = e.sendFor(parent, "task-1", workflow.EventBuilderStuck, "{\"why\":\"the parser needs a design call\"}\n")
		e.restart()
		e.tick()
	}
	if len(e.openIncidents()) != 1 || len(e.decisions()) != 1 || e.delivered()[effectRecoveryDecision] != 1 {
		t.Fatalf("%d incidents, %d queued, %d delivered", len(e.openIncidents()), len(e.decisions()), e.delivered()[effectRecoveryDecision])
	}
}

// Resuming does not raise a budget or reset a count, and recovery does not
// replan: the run stays paused for the decision.
func TestRecoveryResumeCannotRaiseBudgetsOrResetRepairCounts(t *testing.T) {
	e := ownedEnv(t)
	e.startRecovery(true)
	e.changesRequested(1, openFinding)
	e.changesRequested(2, openFinding)
	before := e.countWhere(`SELECT repairs_used FROM budget_groups`)
	budget, _ := e.st.ProviderBudget(ctx, testCollab, "codex")
	for i := 0; i < 3; i++ {
		e.restart()
		if err := e.c.Recover(ctx, e.runID); err != nil {
			t.Fatal(err)
		}
	}
	if st, code := e.runState(); st != "paused" || code != pauseRecovery {
		t.Fatalf("run %s/%s: a restart resumed a run waiting for a decision", st, code)
	}
	after, _ := e.st.ProviderBudget(ctx, testCollab, "codex")
	if e.countWhere(`SELECT repairs_used FROM budget_groups`) != before || *after.InputCeiling != *budget.InputCeiling || after.InputReserved != budget.InputReserved {
		t.Fatal("resuming changed a repair count or a budget")
	}
	if e.attemptsOf("task-1") != 2 {
		t.Fatal("a resume made another attempt")
	}
}

// A person's edit is handed back with reconcile. The whole task is checked and
// reviewed again on the new tree, the edit is counted as an intervention, and no
// repair allowance is created or reset.
func TestReconcileReRunsChecksAndReviewsOnTheNewTree(t *testing.T) {
	e := ownedEnv(t)
	e.startRecovery(true)
	parent := e.stuckAttempt()
	e.tick()
	if len(e.decisions()) != 1 {
		t.Fatal("no decision was requested for a stuck attempt")
	}
	write(t, e.root, "fixed.txt", "a person finished it\n")
	checks := e.chk.calls

	att, err := e.c.Reconcile(ctx, e.runID, "operator", "finished the parser by hand")
	if err != nil {
		t.Fatal(err)
	}
	if att.ParentAttemptID != parent.ID || att.StartSnapshotHash != parent.StartSnapshotHash || e.usedRepairs() != 0 {
		t.Fatalf("reconcile attempt %+v, repairs used %d", att, e.usedRepairs())
	}
	if n, _ := e.st.ManualInterventions(ctx, e.runID); n != 1 {
		t.Fatalf("%d interventions", n)
	}
	if inc := e.openIncidents(); len(inc) != 1 || inc[0].State != "resolved" || inc[0].Resolution != "reconciled" {
		t.Fatalf("incidents %+v", inc)
	}
	for i := 0; i < 4; i++ {
		e.tick()
	}
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	if e.chk.calls != checks+1 || cur.Check != workflow.CheckPassed {
		t.Fatalf("the check ran %d more times and is %s: it must run again on the new tree", e.chk.calls-checks, cur.Check)
	}
	paths, err := e.c.changedPaths(ctx, cur.PatchHash)
	if err != nil || strings.Join(paths, ",") != "fixed.txt,work1.txt" {
		t.Fatalf("the reconciled patch is %v %v: it must hold the stuck attempt's edit and the person's", paths, err)
	}
	if got := e.delivered()[effectReviewPrompt]; got != 1 {
		t.Fatalf("%d review prompts: the new tree is reviewed", got)
	}
	if e.accepted() != 0 {
		t.Fatal("the reconciled tree was accepted without a review")
	}
	if err := e.review(cur, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	e.tick()
	e.tick()
	if e.accepted() != 1 {
		t.Fatal("the reviewed reconcile attempt was not accepted")
	}
	for _, eff := range e.effects(workflow.EffectBuilderDispatch) {
		if eff.AttemptID == att.ID {
			t.Fatal("a reconcile sent a prompt to a builder; nothing is sent for a person's edit")
		}
	}
	if got := e.delivered()[effectRecoveryDecision]; got != 1 {
		t.Fatalf("%d decision requests", got)
	}
}

func TestReconcileRefusals(t *testing.T) {
	t.Run("a run that is not paused", func(t *testing.T) {
		e := newCEnv(t)
		e.startRecovery(true)
		e.tick()
		if _, err := e.c.Reconcile(ctx, e.runID, "operator", "x"); err == nil {
			t.Fatal("reconciled a running run")
		}
	})
	t.Run("no actor or reason", func(t *testing.T) {
		e := newCEnv(t)
		e.startRecovery(true)
		e.stuckAttempt()
		for _, c := range [][2]string{{"", "why"}, {"who", ""}, {" ", " "}} {
			if _, err := e.c.Reconcile(ctx, e.runID, c[0], c[1]); err == nil {
				t.Errorf("reconciled with actor %q and reason %q", c[0], c[1])
			}
		}
		if n, _ := e.st.ManualInterventions(ctx, e.runID); n != 0 {
			t.Fatal("a refused reconcile was counted")
		}
	})
	t.Run("a builder that may still be writing", func(t *testing.T) {
		e := newCEnv(t)
		e.startRecovery(true)
		parent := e.stuckAttempt()
		eff := e.builderEffect(parent)
		e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
		if _, err := e.c.Reconcile(ctx, e.runID, "operator", "x"); err == nil {
			t.Fatal("reconciled while the builder may still write")
		}
		if e.attemptsOf("task-1") != 1 {
			t.Fatal("a refused reconcile made an attempt")
		}
	})
}

// With recovery on, a task that needed its one repair still completes the run
// with exactly one completion notice, after every task is accepted.
func TestRecoveryCompletionNotifiesOnceAfterAllGates(t *testing.T) {
	e := newCEnv(t)
	e.startRecovery(true)
	e.passTask(1, 1)
	if e.delivered()[effectNotify] != 0 {
		t.Fatal("a completion notice before every task was accepted")
	}
	e.passTask(2, 0)
	for i := 0; i < 3; i++ {
		e.tick()
	}
	if st, _ := e.runState(); st != "finished" || e.delivered()[effectNotify] != 1 {
		t.Fatalf("run %s with %d completion notices", st, e.delivered()[effectNotify])
	}
	_ = os.Remove(filepath.Join(e.root, "work1.txt"))
}

// A collaboration stop holds an eligible repair. Nothing is spent, nothing is
// escalated, and resuming the collaboration lets the same repair go ahead once.
func TestRecoveryStopHoldsTheRepairWithoutSpendingOrEscalating(t *testing.T) {
	e := newCEnv(t)
	e.startRecovery(true)
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	write(t, e.root, "work1.txt", "attempt 1\n")
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{}\n")
	e.tick()
	if err := e.st.StopCollaboration(ctx, state.ControlRequest{CollaborationID: testCollab, Actor: "user", Reason: "hold"}); err != nil {
		t.Fatal(err)
	}
	if err := e.review(att, workflow.ReviewStatusChangesRequested, openFinding); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if e.attemptsOf("task-1") != 1 || e.usedRepairs() != 0 || len(e.openIncidents()) != 0 || len(e.decisions()) != 0 {
		t.Fatalf("a stop did not hold the repair cleanly: %d attempts, %d used, %d incidents", e.attemptsOf("task-1"), e.usedRepairs(), len(e.openIncidents()))
	}
	if err := e.st.ResumeCollaboration(ctx, state.ControlRequest{CollaborationID: testCollab, Actor: "user", Reason: "go"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e.tick()
	}
	if e.attemptsOf("task-1") != 2 || e.usedRepairs() != 1 {
		t.Fatalf("after the resume: %d attempts, %d used", e.attemptsOf("task-1"), e.usedRepairs())
	}
}

// A crash after the reconcile attempt exists and before it is submitted is
// finished by the next pass of a restarted controller: one attempt, one
// intervention, the check run once on the new tree.
func TestReconcileCrashBeforeSubmissionConvergesAfterRestart(t *testing.T) {
	e := ownedEnv(t)
	e.startRecovery(true)
	parent := e.stuckAttempt()
	e.tick()
	write(t, e.root, "fixed.txt", "by hand\n")
	e.c.hooks.afterReconcileAttempt = func() error { return errors.New("killed before the submission") }
	if _, err := e.c.Reconcile(ctx, e.runID, "operator", "fixed by hand"); err == nil {
		t.Fatal("the failpoint did not fire")
	}
	if st, _ := e.runState(); st != "paused" {
		t.Fatalf("run %s: nothing was submitted yet", st)
	}
	pending := e.current("task-1")
	if pending.ParentAttemptID != parent.ID || pending.Implementation != workflow.ImplementationPending || pending.Version != 0 {
		t.Fatalf("the half-made attempt: %+v", pending)
	}
	checks := e.chk.calls
	e.restart()
	for i := 0; i < 4; i++ {
		e.tick()
	}
	done, _ := e.st.GetAttempt(ctx, pending.ID)
	if done.Implementation != workflow.ImplementationSubmitted || done.Check != workflow.CheckPassed || e.chk.calls != checks+1 {
		t.Fatalf("after the restart: %s, check %s, %d check runs", done.Implementation, done.Check, e.chk.calls-checks)
	}
	if n, _ := e.st.ManualInterventions(ctx, e.runID); n != 1 || e.attemptsOf("task-1") != 2 {
		t.Fatalf("%d interventions and %d attempts", n, e.attemptsOf("task-1"))
	}
	if st, _ := e.runState(); st != "running" {
		t.Fatalf("run %s after the reconcile was finished", st)
	}
}
