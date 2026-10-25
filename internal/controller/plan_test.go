package controller

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

const dagPlan = `# Receipts

**Goal:** persist receipts.

## Global Constraints

- No new dependencies.

### Task 1: Render
Task-ID: render
Depends-On: receipt-store

Render it.

### Task 2: Persist the receipt
Task-ID: receipt-store
Depends-On: event-envelope
Paths: internal/state
Outputs: receipt-schema

Store the receipt.

### Task 3: Event envelope
Task-ID: event-envelope
Outputs: envelope-schema

Define the envelope.
`

// setPlan replaces the plan file and commits it, so it is not task work.
func (e *cenv) setPlan(text string) {
	e.t.Helper()
	if err := os.WriteFile(e.plan, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
	git(e.t, e.root, "add", ".")
	git(e.t, e.root, "commit", "-qm", "plan edit")
}

// accept plays one task from its first prompt to its acceptance.
func (e *cenv) accept(taskID string, n int) {
	e.t.Helper()
	e.tick()
	att := e.current(taskID)
	e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
	e.work(n)
	e.settle(att)
	e.mustSend(taskID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		e.t.Fatal(err)
	}
	e.tick()
	for _, t := range e.tasks() {
		if t.ID == taskID && t.AcceptedAttemptID != att.ID {
			e.t.Fatalf("task %s was not accepted", taskID)
		}
	}
}

func TestStartImportsStableIdentitiesAndDependencies(t *testing.T) {
	e := newCEnv(t)
	e.setPlan(dagPlan)
	e.start()
	tasks := e.tasks()
	byID := map[string]int{}
	for i, task := range tasks {
		byID[task.ID] = i
	}
	if len(tasks) != 3 || tasks[0].ID != "render" || tasks[1].ID != "receipt-store" || tasks[2].ID != "event-envelope" {
		t.Fatalf("tasks %+v", tasks)
	}
	for i, task := range tasks {
		if task.Ordinal != i+1 {
			t.Errorf("%s ordinal %d: the plan's order is stored apart from identity", task.ID, task.Ordinal)
		}
	}
	if strings.Join(tasks[0].DependsOn, ",") != "receipt-store" || strings.Join(tasks[1].DependsOn, ",") != "event-envelope" || len(tasks[2].DependsOn) != 0 {
		t.Errorf("dependencies %v %v %v", tasks[0].DependsOn, tasks[1].DependsOn, tasks[2].DependsOn)
	}

	// The spec artifact carries what the row cannot, and the brief carries none of the metadata.
	raw, err := e.art.ReadAll(ctx, tasks[1].SpecHash, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var spec workflow.TaskSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ID != "receipt-store" || strings.Join(spec.Paths, ",") != "internal/state" || strings.Join(spec.Outputs, ",") != "receipt-schema" {
		t.Errorf("spec %+v", spec)
	}
	cons, _ := e.art.ReadAll(ctx, tasks[1].ConstraintsHash, 1<<20)
	if !strings.Contains(string(cons), "No new dependencies.") {
		t.Errorf("the task's constraints lost the plan's preamble: %q", cons)
	}
	brief, _ := e.art.ReadAll(ctx, tasks[1].BriefHash, 1<<20)
	if !strings.Contains(string(brief), "Store the receipt.") || strings.Contains(string(brief), "Depends-On") || strings.Contains(string(brief), "Task-ID") {
		t.Errorf("brief %q", brief)
	}
}

func TestLegacyPlanStartsWithOrdinalIdsAndAChain(t *testing.T) {
	e := newCEnv(t)
	e.start()
	tasks := e.tasks()
	if len(tasks) != 2 || tasks[0].ID != "task-1" || tasks[1].ID != "task-2" || len(tasks[0].DependsOn) != 0 || strings.Join(tasks[1].DependsOn, ",") != "task-1" {
		t.Fatalf("%+v", tasks)
	}
	cons, _ := e.art.ReadAll(ctx, tasks[0].ConstraintsHash, 1<<20)
	if !strings.Contains(string(cons), "**Goal:** greet.") {
		t.Errorf("a legacy plan's whole preamble is the constraints: %q", cons)
	}
}

func TestStartRefusesAnInvalidPlanBeforeCreatingAnything(t *testing.T) {
	e := newCEnv(t)
	e.setPlan("# P\n\n### Task 1: A\nTask-ID: a\nDepends-On: b\n\nx\n\n### Task 2: B\nTask-ID: b\nDepends-On: a\n\ny\n")
	_, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderSession, r.PlannerSession, r.CheckCommand = "b", "p", "" }))
	if err == nil || !strings.Contains(err.Error(), "a -> b -> a") {
		t.Fatalf("err %v", err)
	}
	if ar, _ := ReadActiveRun(e.relay); ar != nil {
		t.Fatal("a refused plan left a marker")
	}
	// Nothing was created, so the checkout is free for a valid plan.
	e.setPlan(twoTaskPlan)
	e.start()
}

func TestDriveStartsTheFirstReadyTaskNotTheFirstListed(t *testing.T) {
	e := newCEnv(t)
	e.setPlan(dagPlan)
	e.start()
	e.tick()
	// "render" is listed first but waits for the others; the chain is
	// event-envelope, then receipt-store, then render.
	for _, id := range []string{"render", "receipt-store"} {
		if as, _ := e.st.TaskAttempts(ctx, e.runID, id); len(as) != 0 {
			t.Fatalf("task %s started before its dependencies were accepted", id)
		}
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "event-envelope"); len(as) != 1 {
		t.Fatalf("the ready task has %d attempts", len(as))
	}
	e.accept("event-envelope", 1)
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "render"); len(as) != 0 {
		t.Fatal("render started while receipt-store was not accepted")
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "receipt-store"); len(as) != 1 {
		t.Fatal("receipt-store did not start after its dependency was accepted")
	}
	e.accept("receipt-store", 2)
	e.accept("render", 3)
	if st, _ := e.runState(); st != "finished" {
		t.Errorf("run is %s", st)
	}
	if got := e.delivered()["notify_planner"]; got != 1 {
		t.Errorf("%d final notices", got)
	}
}

func TestPlanHashChangeRefusesSilentResume(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	before := len(e.tr.sends)
	if before != 1 {
		t.Fatalf("%d prompts before the edit", before)
	}

	// The plan is edited and its tasks are reordered.
	i2 := strings.Index(twoTaskPlan, "### Task 2")
	e.setPlan(twoTaskPlan[:strings.Index(twoTaskPlan, "### Task 1")] + twoTaskPlan[i2:] + "\n" + twoTaskPlan[strings.Index(twoTaskPlan, "### Task 1"):i2])

	err := e.c.Recover(ctx, e.runID)
	if !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("Recover: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != "plan_changed" {
		t.Fatalf("run is %s/%s", st, code)
	}
	if !strings.Contains(err.Error(), "reconcil") {
		t.Errorf("the error must say what to do: %v", err)
	}
	e.tick()
	if err := e.c.Recover(ctx, e.runID); !errors.Is(err, ErrPlanChanged) {
		t.Errorf("a second Recover: %v", err)
	}
	if len(e.tr.sends) != before {
		t.Fatalf("%d prompts were sent after the plan changed", len(e.tr.sends)-before)
	}

	// Restoring the plan is the one way back: the same bytes are the same plan.
	e.setPlan(twoTaskPlan)
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatalf("Recover with the plan restored: %v", err)
	}
	if st, code := e.runState(); st != "running" || code != "" {
		t.Errorf("run is %s/%s after the plan came back", st, code)
	}
}

func TestPlanThatCannotBeReadRefusesResume(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	if err := os.Remove(e.plan); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Recover(ctx, e.runID); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("Recover: %v", err)
	}
	if st, code := e.runState(); st != "paused" || code != "plan_changed" {
		t.Errorf("run is %s/%s", st, code)
	}
}

func TestEditingThePlanDoesNotStopARunningTick(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	// A planner ticking a box in the plan is not a resume.
	e.setPlan(strings.Replace(twoTaskPlan, "Do the first thing.", "Do the first thing. (done: nothing yet)", 1))
	e.tick()
	if st, _ := e.runState(); st != "running" {
		t.Errorf("a tick paused the run after a plan edit: %s", st)
	}
}

func TestStopStillWorksWhenThePlanChanged(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	e.setPlan(twoTaskPlan + "\nExtra.\n")
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.runState(); st != "stopping" {
		t.Errorf("run is %s", st)
	}
}
