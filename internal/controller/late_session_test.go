package controller

import (
	"testing"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// submitObserved plays one task's builder to a submission, with the builder's
// pane observed as obs when the submission comes in.
func (e *cenv) submitObserved(obs runner.Observation) workflow.Attempt {
	e.t.Helper()
	e.tick()
	att := e.current("task-1")
	eff := e.builderEffect(att)
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	obs.State, obs.Nonce = runner.ObsCompleted, eff.Nonce
	e.tr.set(eff.ID, obs)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	return att
}

// A Codex builder creates its conversation at its first model call, so a fresh
// pane has no session id when the task is sent. By submission it has worked,
// so the session is read then and the usage found by it.
func TestASessionLearnedAtSubmissionMakesTheTasksUsageKnown(t *testing.T) {
	e, src := usageEnv(t)
	e.tr.noSession = true
	e.startLimited(1_000_000, 0)
	src.streams["late-thread"] = []rollout.Stream{stream("late-thread", "", ev("late-thread", 2, 100, 0, 5))}

	att := e.submitObserved(runner.Observation{ExternalID: "late-thread", Session: "late-thread"})

	if e.pause() != "running/" || e.reviewerJobs(att) != 1 {
		t.Fatalf("run is %s with %d reviewer jobs: the task's usage was not found by its late session", e.pause(), e.reviewerJobs(att))
	}
	if u := e.total(t); u.Completeness != workflow.UsageComplete || u.Input == nil || *u.Input != 100 {
		t.Errorf("usage %+v", u)
	}
}

// A pane id is what Herdr reports for an agent with no session. It names no
// conversation and no log, so it is never taken for the builder's session.
func TestAPaneIDIsNeverTakenForTheBuildersSession(t *testing.T) {
	e, src := usageEnv(t)
	e.tr.noSession = true
	e.startLimited(1_000_000, 0)
	src.streams["w8:p2W"] = []rollout.Stream{stream("w8:p2W", "", ev("w8:p2W", 2, 100, 0, 5))}

	att := e.submitObserved(runner.Observation{ExternalID: "w8:p2W"})

	if e.pause() != "paused/scheduling_blocked" || e.reviewerJobs(att) != 0 {
		t.Errorf("run is %s: usage was read from a pane id", e.pause())
	}
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "builder")
	if err != nil || len(jobs) != 1 || jobs[0].SessionID != "" {
		t.Errorf("builder jobs %+v %v", jobs, err)
	}
}

// Usage read while the task runs finds no session and records a gap. Once the
// session is known the gap is history, not a missing log: it must not keep the
// task's usage unknown.
func TestAGapRecordedBeforeTheSessionWasKnownClearsOnceItIs(t *testing.T) {
	e, src := usageEnv(t)
	e.tr.noSession = true
	e.startLimited(1_000_000, 0)
	src.streams["late-thread"] = []rollout.Stream{stream("late-thread", "", ev("late-thread", 2, 100, 0, 5))}
	e.tick()
	if u := e.total(t); u.Completeness != workflow.UsageUnavailable {
		t.Fatalf("before the session is known, usage %+v", u)
	}

	att := e.submitObserved(runner.Observation{ExternalID: "late-thread", Session: "late-thread"})

	if e.pause() != "running/" || e.reviewerJobs(att) != 1 {
		t.Fatalf("run is %s with %d reviewer jobs", e.pause(), e.reviewerJobs(att))
	}
	if u := e.total(t); u.Completeness != workflow.UsageComplete || u.Input == nil || *u.Input != 100 {
		t.Errorf("usage %+v", u)
	}
}

// Herdr learns a Codex session only from a hook Codex runs. When it does not
// run, the task's conversation is found by the nonce its prompt carried.
func TestASessionHerdrNeverLearnsIsFoundByTheTasksNonce(t *testing.T) {
	e, src := usageEnv(t)
	e.tr.noSession = true
	e.startLimited(1_000_000, 0)
	e.tick()
	nonce := e.builderEffect(e.current("task-1")).Nonce
	src.prompts = map[string]string{nonce: "found-thread"}
	src.streams["found-thread"] = []rollout.Stream{stream("found-thread", "", ev("found-thread", 2, 100, 0, 5))}

	att := e.submitObserved(runner.Observation{ExternalID: "w8:p2W"})

	if e.pause() != "running/" || e.reviewerJobs(att) != 1 {
		t.Fatalf("run is %s with %d reviewer jobs", e.pause(), e.reviewerJobs(att))
	}
	if u := e.total(t); u.Completeness != workflow.UsageComplete || u.Input == nil || *u.Input != 100 {
		t.Errorf("usage %+v", u)
	}
}
