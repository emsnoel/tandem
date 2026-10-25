package controller

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// passTask drives the current attempt of task n from its prompt to an accepted
// review. A changes_requested review first makes the controller start a repair
// attempt, which is then driven to a pass.
func (e *cenv) passTask(n int, repairs int) {
	e.t.Helper()
	task := e.tasks()[n-1]
	for round := 0; round <= repairs; round++ {
		e.tick()
		att := e.current(task.ID)
		e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
		write(e.t, e.root, fmt.Sprintf("work%d.txt", n), fmt.Sprintf("task %d round %d\n", n, round))
		e.settle(att)
		e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
		e.tick()
		status := workflow.ReviewStatusPassed
		var findings []workflow.Finding
		if round < repairs {
			status = workflow.ReviewStatusChangesRequested
			findings = []workflow.Finding{{Severity: "high", Path: "work.txt", Disposition: "open", Message: "redo"}}
		}
		if err := e.review(att, status, findings...); err != nil {
			e.t.Fatal(err)
		}
		e.tick()
	}
	if e.tasks()[n-1].AcceptedAttemptID == "" {
		e.t.Fatalf("task %d was not accepted", n)
	}
}

// builderJobStates lists the state of every builder job of the run.
func (e *cenv) builderJobStates() []string {
	e.t.Helper()
	var out []string
	for _, task := range e.tasks() {
		as, err := e.st.TaskAttempts(ctx, e.runID, task.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, a := range as {
			jobs, err := e.st.AttemptJobs(ctx, a.ID, "builder")
			if err != nil {
				e.t.Fatal(err)
			}
			for _, j := range jobs {
				out = append(out, j.State)
			}
		}
	}
	return out
}

func allIn(states []string, want string) bool {
	for _, s := range states {
		if s != want {
			return false
		}
	}
	return len(states) > 0
}

func TestBuilderJobsEndWhenTheirAttemptIsAcceptedOrReplaced(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.passTask(1, 1) // one repair: the first attempt is replaced, the second accepted
	e.passTask(2, 0)
	if state, _ := e.runState(); state != "finished" {
		t.Fatalf("run %s", state)
	}
	if got := e.builderJobStates(); len(got) != 3 || !allIn(got, "completed") {
		t.Fatalf("builder jobs of a finished run are %v; each attempt's job ends when the attempt is accepted or replaced", got)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		t.Fatalf("a finished run must be archivable: %v", err)
	}
}

func TestAStopThatSettlesCancelsTheBuilderJobItInterrupted(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	att := e.current("task-1")
	eff := e.builderEffect(att)
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.builderJobStates(); !allIn(got, "running") {
		t.Fatalf("the builder job ended while its builder was still working: %v", got)
	}
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsStopped, Nonce: eff.Nonce})
	e.tick()
	if state, code := e.runState(); state != "paused" || code != pauseStopped {
		t.Fatalf("run %s/%s", state, code)
	}
	if got := e.builderJobStates(); !allIn(got, "cancelled") {
		t.Fatalf("the builder job of a stopped run is %v", got)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		t.Fatalf("a stopped, settled run must be archivable: %v", err)
	}
}

// reopenBuilderJobs puts the run's builder jobs back to running, as a tandem
// without the settling rule left them.
func (e *cenv) reopenBuilderJobs() {
	e.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(e.stRoot, "state.sqlite"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET state='running', completed_ms=NULL WHERE kind='builder'`); err != nil {
		e.t.Fatal(err)
	}
}

func TestRecoverEndsTheBuilderJobsAnOlderTandemLeftOpen(t *testing.T) {
	t.Run("finished", func(t *testing.T) {
		e := newCEnv(t)
		e.start()
		e.passTask(1, 1)
		e.passTask(2, 0)
		e.reopenBuilderJobs()
		if err := e.st.ArchiveRun(ctx, e.runID); !errors.Is(err, workflow.ErrBusy) {
			t.Fatalf("setup: archive with open builder jobs: %v", err)
		}
		if err := e.c.Recover(ctx, e.runID); err != nil {
			t.Fatal(err)
		}
		if got := e.builderJobStates(); !allIn(got, "completed") {
			t.Fatalf("after recover the builder jobs are %v", got)
		}
		if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
			t.Fatalf("recover must leave a finished run archivable: %v", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		e := newCEnv(t)
		e.start()
		e.passTask(1, 0)
		e.tick() // task 2's prompt is sent
		att := e.current("task-2")
		eff := e.builderEffect(att)
		e.mustSend("task-2", workflow.EventBuilderStarted, "{}\n")
		e.tr.set(eff.ID, runner.Observation{State: runner.ObsStopped, Nonce: eff.Nonce})
		if err := e.c.Stop(ctx, e.runID); err != nil {
			t.Fatal(err)
		}
		e.tick()
		e.reopenBuilderJobs()
		if err := e.c.Recover(ctx, e.runID); err != nil {
			t.Fatal(err)
		}
		if got := e.builderJobStates(); len(got) != 2 || got[0] != "completed" || got[1] != "cancelled" {
			t.Fatalf("after recover: %v; the accepted attempt's job completes and the interrupted one is cancelled", got)
		}
		if state, code := e.runState(); state != "paused" || code != pauseStopped {
			t.Fatalf("recover must not resume a stopped run: %s/%s", state, code)
		}
		if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
			t.Fatalf("recover must leave a stopped run archivable: %v", err)
		}
	})
}
