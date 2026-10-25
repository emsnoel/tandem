package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/workflow"
)

// startLimited starts the two-task plan with budget limits.
func (e *cenv) startLimited(task, run int64) {
	e.t.Helper()
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.TaskInputLimit, r.RunInputLimit = task, run }))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
}

// spend makes the attempt's session log say it has used input tokens in all. A
// logged event never changes, so more use is a new event, as in a real log.
func (e *cenv) spend(src *fakeUsage, att workflow.Attempt, input int64) {
	s := e.sessionOf(att)
	if e.spent == nil {
		e.spent, e.lines = map[string]int64{}, map[string]int{}
	}
	var events []rollout.Delta
	for _, st := range src.streams[s] {
		events = st.Deltas
	}
	if input > e.spent[s] {
		e.lines[s]++
		events = append(append([]rollout.Delta{}, events...), ev(s, 2+e.lines[s], input-e.spent[s], 0, 1))
		e.spent[s] = input
	}
	src.streams[s] = []rollout.Stream{stream(s, "", events...)}
}

func (e *cenv) reviewerJobs(att workflow.Attempt) int {
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	return len(jobs)
}

func (e *cenv) pause() string { st, code := e.runState(); return st + "/" + code }

func TestBudgetBlocksNewModelWorkButNotChecks(t *testing.T) {
	e, src := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 1000) // the run's limit, reached
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()

	if e.chk.calls != 1 || e.current("task-1").Check != workflow.CheckPassed {
		t.Errorf("a check is not model work and must still run: %d calls, check %s", e.chk.calls, e.current("task-1").Check)
	}
	if e.reviewerJobs(att) != 0 || len(e.effects("review_prompt")) != 0 || e.delivered()["review_prompt"] != 0 {
		t.Error("a review was started with the budget exhausted")
	}
	if e.pause() != "paused/scheduling_blocked" {
		t.Errorf("run is %s", e.pause())
	}
	if e.delivered()[workflow.EffectBuilderDispatch] != 1 {
		t.Error("the implementation was sent again")
	}
}

func TestBudgetIncreaseResumesPendingReview(t *testing.T) {
	e, src := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 1000)
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if e.pause() != "paused/scheduling_blocked" || e.reviewerJobs(att) != 0 {
		t.Fatalf("fixture: %s", e.pause())
	}

	d, err := e.c.SetBudget(ctx, e.runID, "run", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if !d.SchedulingAllowed || e.pause() != "running/" {
		t.Fatalf("after raising the limit: %+v, run %s", d, e.pause())
	}
	e.tick()
	e.tick()
	if e.reviewerJobs(att) != 1 || e.delivered()["review_prompt"] != 1 {
		t.Errorf("%d reviewer jobs, %d review prompts: the pending review runs once", e.reviewerJobs(att), e.delivered()["review_prompt"])
	}
	if e.delivered()[workflow.EffectBuilderDispatch] != 1 || e.chk.calls != 1 {
		t.Errorf("work already done was repeated: %d dispatches, %d checks", e.delivered()[workflow.EffectBuilderDispatch], e.chk.calls)
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-1"); len(as) != 1 {
		t.Errorf("%d attempts", len(as))
	}
}

func TestBudgetAcceptanceIndependent(t *testing.T) {
	e, src := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 400) // room enough for the review
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	// The budget is spent before the finished, checked and reviewed task is accepted.
	e.spend(src, att, 2000)
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Fatal("valid, verified work was not accepted because the budget ran out")
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-2"); len(as) != 0 {
		t.Error("the next task started with the budget exhausted")
	}
	if e.pause() != "paused/scheduling_blocked" {
		t.Errorf("run is %s", e.pause())
	}
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Error("acceptance was undone")
	}
	// Raising the limit starts the next task, once.
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 100000); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-2"); len(as) != 1 {
		t.Errorf("%d attempts of task 2", len(as))
	}
}

func TestBudgetTaskAndRunLimitsAreIndependentInTheController(t *testing.T) {
	e, src := usageEnv(t)
	e.startLimited(300, 100000) // the task limit is the small one
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 300)
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if e.pause() != "paused/scheduling_blocked" || e.reviewerJobs(att) != 0 {
		t.Fatalf("the task limit did not block: %s", e.pause())
	}
	// Raising the run limit does not help: the task is what is over.
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 999999); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.reviewerJobs(att) != 0 || e.pause() != "paused/scheduling_blocked" {
		t.Errorf("raising the wrong limit unblocked the review: %s", e.pause())
	}
	if _, err := e.c.SetBudget(ctx, e.runID, "task", 5000); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.reviewerJobs(att) != 1 {
		t.Errorf("%d reviewer jobs after raising the task limit", e.reviewerJobs(att))
	}
}

func TestBudgetUnknownWithLimitPauses(t *testing.T) {
	// A limit is configured and the usage cannot be read: the run does not guess.
	e, _ := usageEnv(t)
	e.startLimited(0, 1_000_000)
	att := e.toReview("task-1", 1)
	if e.pause() != "paused/scheduling_blocked" || e.reviewerJobs(att) != 0 {
		t.Errorf("run is %s with %d reviewer jobs", e.pause(), e.reviewerJobs(att))
	}
	// Partial counts are not enough either.
	p, src := usageEnv(t)
	p.startLimited(0, 1_000_000)
	p.tick()
	pa := p.current("task-1")
	s := p.sessionOf(pa)
	cut := stream(s, "", ev(s, 2, 10, 0, 1))
	cut.Truncated = true
	src.streams[s] = []rollout.Stream{cut}
	p.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	p.work(1)
	p.settle(pa)
	p.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	p.tick()
	if p.pause() != "paused/scheduling_blocked" || p.reviewerJobs(pa) != 0 {
		t.Errorf("partial usage under a limit: %s", p.pause())
	}

	// With no limit the same unknown usage is recorded and the run carries on.
	u, _ := usageEnv(t)
	u.start()
	ua := u.toReview("task-1", 1)
	if u.pause() != "running/" || u.reviewerJobs(ua) != 1 {
		t.Errorf("an unlimited run stopped for unknown usage: %s", u.pause())
	}
}

func TestBudgetRepairWaitsForBudget(t *testing.T) {
	e, src := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 400)
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	// The review finds problems, and by then the budget is gone.
	if err := e.review(att, workflow.ReviewStatusChangesRequested, workflow.Finding{Severity: "high", Path: "a.go", Disposition: "open", Message: "wrong"}); err != nil {
		t.Fatal(err)
	}
	e.spend(src, att, 1500)
	e.tick()
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-1"); len(as) != 1 {
		t.Fatalf("a repair started with the budget exhausted: %d attempts", len(as))
	}
	if e.pause() != "paused/scheduling_blocked" {
		t.Errorf("run is %s", e.pause())
	}
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 100000); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-1"); len(as) != 2 {
		t.Errorf("%d attempts after the budget was raised: the repair runs once", len(as))
	}
}

func TestBudgetWarningDoesNotBlock(t *testing.T) {
	e, src := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 800) // 80%: a warning, not a stop
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if e.reviewerJobs(att) != 1 || e.pause() != "running/" {
		t.Errorf("a warning stopped the run: %s, %d reviewer jobs", e.pause(), e.reviewerJobs(att))
	}
}

func TestBudgetPolicyIsReadFromTheRunNotTheEnvironment(t *testing.T) {
	e, src := usageEnv(t)
	e.owner = "the-controller"
	e.c = e.newController()
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	e.spend(src, att, 1000)
	// A new process, built with nothing that says what the limits are.
	e.c = e.newController()
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if e.pause() != "paused/scheduling_blocked" {
		t.Fatalf("a restarted controller forgot the limit: %s", e.pause())
	}
	run, _ := e.st.GetRun(ctx, e.runID)
	if run.RunInputLimit != 1000 || run.TaskInputLimit != 0 {
		t.Errorf("%+v", run)
	}
	// A raised limit survives a restart too.
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 9000); err != nil {
		t.Fatal(err)
	}
	e.c = e.newController()
	if run, _ := e.st.GetRun(ctx, e.runID); run.RunInputLimit != 9000 {
		t.Errorf("%+v", run)
	}
	e.tick()
	if e.reviewerJobs(att) != 1 {
		t.Errorf("%d reviewer jobs", e.reviewerJobs(att))
	}
}

func TestSetBudgetRefusesBadInput(t *testing.T) {
	e, _ := usageEnv(t)
	e.startLimited(0, 1000)
	for name, c := range map[string]struct {
		scope string
		limit int64
	}{"negative": {"run", -1}, "unknown scope": {"everything", 5}, "empty scope": {"", 5}} {
		if _, err := e.c.SetBudget(ctx, e.runID, c.scope, c.limit); !errors.Is(err, workflow.ErrInvalidTransition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.c.SetBudget(ctx, strings.Repeat("0", 32), "run", 5); err == nil {
		t.Error("an unknown run")
	}
	if run, _ := e.st.GetRun(ctx, e.runID); run.RunInputLimit != 1000 {
		t.Errorf("a refused change moved the limit: %+v", run)
	}
	// Zero turns a limit off.
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 0); err != nil {
		t.Fatal(err)
	}
	if run, _ := e.st.GetRun(ctx, e.runID); run.RunInputLimit != 0 {
		t.Errorf("%+v", run)
	}
}

func TestBudgetChangesAreRecordedAsEventsOnTheAttempt(t *testing.T) {
	e, _ := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	att := e.current("task-1")
	before, _ := e.st.GetAttempt(ctx, att.ID)
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 2000); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.GetAttempt(ctx, att.ID)
	if after.Version <= before.Version {
		t.Errorf("a policy change must be an event in the attempt's history: version %d -> %d", before.Version, after.Version)
	}
	if after.Implementation != before.Implementation || after.Check != before.Check || after.Review != before.Review {
		t.Errorf("a budget change moved the work: %+v -> %+v", before, after)
	}
	// Repeating the same change adds nothing.
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 2000); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.st.GetAttempt(ctx, att.ID); again.Version != after.Version {
		t.Errorf("version %d -> %d for no change", after.Version, again.Version)
	}
}

func TestRaisingALimitOnlyResumesARunPausedForScheduling(t *testing.T) {
	e, _ := usageEnv(t)
	e.startLimited(0, 1000)
	e.tick()
	// The builder says it is stuck: a pause that a person must deal with, not a budget.
	e.mustSend("task-1", workflow.EventBuilderStuck, "{\"why\":\"cannot\"}\n")
	if e.pause() != "paused/stuck" {
		t.Fatalf("fixture: %s", e.pause())
	}
	if _, err := e.c.SetBudget(ctx, e.runID, "run", 999999); err != nil {
		t.Fatal(err)
	}
	if e.pause() != "paused/stuck" {
		t.Errorf("a budget change resumed a run that was paused for another reason: %s", e.pause())
	}
}

func TestSettingABudgetBeforeAnyWorkSaysAvailableNotUnknown(t *testing.T) {
	e, _ := usageEnv(t)
	e.startLimited(0, 0)
	d, err := e.c.SetBudget(ctx, e.runID, "run", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing has been sent, so nothing has been used: not an unknown to pause on.
	if d.State != workflow.BudgetAvailable || !d.SchedulingAllowed {
		t.Errorf("%+v", d)
	}
	e.tick()
	if e.pause() != "running/" || e.delivered()[workflow.EffectBuilderDispatch] != 1 {
		t.Errorf("run is %s", e.pause())
	}
}
