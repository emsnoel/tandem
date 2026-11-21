package controller

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// buildTask plays the builder for task n: the prompt is delivered, the builder
// acknowledges it, writes its file and hands back, and the check and review
// prompt follow.
func (e *cenv) buildTask(n int) workflow.Attempt {
	e.t.Helper()
	task := e.tasks()[n-1]
	e.tick()
	att := e.current(task.ID)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.work(n)
	e.settle(att)
	e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	return att
}

// acceptTask drives task n to acceptance.
func (e *cenv) acceptTask(n int) workflow.Attempt {
	e.t.Helper()
	att := e.buildTask(n)
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		e.t.Fatal(err)
	}
	e.tick()
	if got := e.tasks()[n-1].AcceptedAttemptID; got != att.ID {
		e.t.Fatalf("task %d was not accepted: %q", n, got)
	}
	return att
}

// stopAndArchive has the builder take the prompt it was sent last, then stops
// the run, ends its open jobs and archives it, as the pause note's steps do.
func (e *cenv) stopAndArchive(taskID string) string {
	e.t.Helper()
	e.tick()
	att := e.current(taskID)
	if eff := e.builderEffect(att); eff.State != "acknowledged" {
		e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
		e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted})
	}
	e.stopToPaused()
	if err := e.c.Recover(ctx, e.runID); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ArchiveRun(ctx, e.runID); err != nil {
		e.t.Fatal(err)
	}
	return e.runID
}

func (e *cenv) readArtifact(hash string) string {
	e.t.Helper()
	raw, err := e.art.ReadAll(ctx, hash, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

func (e *cenv) resume(from string) (string, error) {
	e.t.Helper()
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ResumeFrom = from }))
	if err == nil {
		e.runID = id
	}
	return id, err
}

func TestResumeStartsAtTheTaskTheRunStoppedOn(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.start()
	first := e.acceptTask(1)
	old := e.stopAndArchive("task-2")

	sends := len(e.tr.sends)
	run, err := e.resume(ResumeLatest)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if run == old {
		t.Fatal("the resume did not start a new run")
	}
	tasks := e.tasks()
	if tasks[0].AcceptedAttemptID != first.ID || tasks[1].AcceptedAttemptID != "" {
		t.Fatalf("carried %+v", tasks)
	}
	if from, err := RunResumedFrom(ctx, e.st, e.art, run); err != nil || from != old {
		t.Fatalf("resumed_from %q %v", from, err)
	}
	if as, _ := e.st.TaskAttempts(ctx, run, "task-1"); len(as) != 0 {
		t.Fatalf("the carried task got %d attempts in the new run", len(as))
	}
	e.tick()
	got := e.tr.sends[sends:]
	if len(got) != 1 || got[0].Kind != workflow.EffectBuilderDispatch || !strings.Contains(e.readArtifact(got[0].PromptHash), "Implement task 2 of 2") {
		t.Fatalf("the resumed run sent %+v", got)
	}

	second := e.buildTask(2)
	if err := e.review(second, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if st, code := e.runState(); st != "finished" {
		t.Fatalf("run %s/%s", st, code)
	}
	ev, err := e.c.TeardownEvidence(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range ev.Tasks {
		if len(task.Unmet) != 0 {
			t.Fatalf("task %s lacks %v", task.ID, task.Unmet)
		}
	}
}

func TestResumeCarriesTheOpenFindingsOfATaskUnderRepair(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.start()
	att := e.buildTask(1)
	line := 1
	finding := workflow.Finding{Severity: workflow.SeverityHigh, Path: "work1.txt", Disposition: "open", Message: "the greeting has no full stop", StartLine: &line, EndLine: &line}
	if err := e.review(att, workflow.ReviewStatusChangesRequested, finding); err != nil {
		t.Fatal(err)
	}
	e.tick() // starts the repair
	old := e.stopAndArchive("task-1")

	if _, err := e.resume(old); err != nil {
		t.Fatalf("resume: %v", err)
	}
	e.tick()
	cur := e.current("task-1")
	if cur.Ordinal != 1 || cur.ParentAttemptID != "" {
		t.Fatalf("the resumed attempt %+v", cur)
	}
	prompt := e.readArtifact(cur.PacketHash)
	for _, want := range []string{"resumes run " + old, "asked for changes to attempt 1 of that run", "the greeting has no full stop", "work1.txt"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestResumeRefusesWhatItCannotCarry(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	if _, err := e.resume(ResumeLatest); !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), "no archived run") {
		t.Fatalf("no archived run: %v", err)
	}
	e.start()
	e.acceptTask(1)
	running := e.runID
	if _, err := e.resume(running); !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), "only an archived run") {
		t.Fatalf("a running run: %v", err)
	}
	e.runID = running
	old := e.stopAndArchive("task-2")

	write(t, e.root, "stray.txt", "not the run's\n")
	if _, err := e.resume(ResumeLatest); !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), "task-1 was accepted on") {
		t.Fatalf("a changed tree: %v", err)
	}
	if err := os.Remove(filepath.Join(e.root, "stray.txt")); err != nil {
		t.Fatal(err)
	}

	_, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ResumeFrom, r.CheckCommand = old, "make test" }))
	if !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), "make test") {
		t.Fatalf("another check command: %v", err)
	}
	if _, err := e.resume("not-a-run"); !errors.Is(err, ErrCannotResume) {
		t.Fatalf("a bad id: %v", err)
	}
	if n, _ := e.st.CountRuns(ctx); n != 1 {
		t.Fatalf("a refused resume created a run: %d runs", n)
	}

	// The refusals created nothing, so the resume still goes through.
	if _, err := e.resume(ResumeLatest); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestResumeRefusesARunWhoseEvidenceNoLongerHolds(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.start()
	first := e.acceptTask(1)
	e.stopAndArchive("task-2")
	if _, err := e.raw().Exec(`DELETE FROM review_results WHERE attempt_id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resume(ResumeLatest); !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), "no longer has the evidence") {
		t.Fatalf("a missing review: %v", err)
	}
}
