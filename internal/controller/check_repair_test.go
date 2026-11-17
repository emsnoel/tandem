package controller

import (
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// submit runs one builder turn for the task's current attempt and returns it.
func (e *cenv) submit(taskID string, n int) workflow.Attempt {
	e.t.Helper()
	att := e.current(taskID)
	e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
	e.work(n)
	e.settle(att)
	e.mustSend(taskID, workflow.EventBuilderSubmitted, "{}\n")
	return att
}

// A check that fails goes back to the builder like a reviewer's request for
// changes: a new attempt of the same task, with the end of the check's log in
// its prompt. When that attempt passes, the task is accepted and the run never
// pauses.
func TestAFailedCheckGoesBackToTheBuilderAsARepair(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.chk.exit = 1
	e.tick()
	first := e.submit("task-1", 1)
	e.tick()
	if st, code := e.runState(); st != "running" {
		t.Fatalf("run %s/%s after a failed check, want running", st, code)
	}
	if got := e.delivered()["review_prompt"]; got != 0 {
		t.Fatal("a review was requested for a check that did not pass")
	}
	cur := e.current("task-1")
	if cur.ID == first.ID || cur.Ordinal != 2 || cur.ParentAttemptID != first.ID {
		t.Fatalf("the repair must be attempt 2 of the same task, linked to attempt 1: %+v", cur)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 2 {
		t.Fatalf("%d builder prompts sent, want 2", got)
	}
	cert, found, err := e.st.AttemptCheckCertificate(ctx, first.ID)
	if err != nil || !found {
		t.Fatalf("no check certificate for attempt 1: %v", err)
	}
	prompt, _ := e.packet(cur)
	for _, want := range []string{"This is attempt 2", "The check failed on attempt 1", "fresh conversation", "log of ", cert.LogHash, "Files attempt 1 changed: work1.txt"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the repair prompt is missing %q:\n%s", want, prompt)
		}
	}
	if res, err := e.read(cur.ID, cert.LogHash, 0, 0); err != nil || !strings.Contains(res.Text, "log of ") {
		t.Errorf("the repair cannot read the check log: %+v %v", res, err)
	}

	e.chk.exit = 0
	second := e.submit("task-1", 1)
	e.tick()
	if st, code := e.runState(); st != "running" {
		t.Fatalf("run %s/%s after attempt 2's check", st, code)
	}
	if got := e.current("task-1"); got.Check != workflow.CheckPassed {
		t.Fatalf("check %s on attempt 2", got.Check)
	}
	if err := e.review(second, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != second.ID {
		t.Fatal("attempt 2 was not accepted")
	}
	if st, code := e.runState(); st != "running" {
		t.Fatalf("run %s/%s after the task was accepted", st, code)
	}
}

// A check that keeps failing uses the task's repairs like a reviewer's
// requests for changes do, and then the run pauses with check_failed.
func TestAFailedCheckPausesOnlyWhenTheRepairsAreUsedUp(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.chk.exit = 1
	for n := 1; n <= 3; n++ {
		e.tick()
		if att := e.current("task-1"); att.Ordinal != n {
			t.Fatalf("attempt ordinal %d, want %d", att.Ordinal, n)
		}
		if st, code := e.runState(); st != "running" {
			t.Fatalf("run %s/%s before attempt %d's submission", st, code, n)
		}
		e.submit("task-1", 1)
	}
	e.tick()
	if st, code := e.runState(); st != "paused" || code != workflow.PauseCheckFailed {
		t.Fatalf("run %s/%s after three failed checks, want paused/%s", st, code, workflow.PauseCheckFailed)
	}
	if e.attemptsOf("task-1") != 3 {
		t.Fatalf("%d attempts, want 3", e.attemptsOf("task-1"))
	}
	calls := e.chk.calls
	e.tick()
	if e.chk.calls != calls {
		t.Fatal("a paused run ran the check again; checks must not loop")
	}
}
