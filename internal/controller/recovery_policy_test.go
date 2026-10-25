package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const goodRecoveryPolicy = `{
 "schema_version": 1,
 "name": "bounded-v1",
 "automatic_repair": {"kinds": ["declared_local_check", "review_changes_requested"]},
 "max_automatic_repairs_per_work": 1,
 "escalate_to": "planner",
 "replanning": "none"
}`

func TestRecoveryAllowlist(t *testing.T) {
	for _, kind := range []string{"unknown", "infra", "missing_prerequisite", "external_service", "delivery_unknown", "stuck", "review_failed"} {
		if automaticRepairEligible(kind, true, true) {
			t.Fatalf("allowed %s", kind)
		}
	}
	if !automaticRepairEligible("declared_local_check", true, true) || !automaticRepairEligible("review_changes_requested", true, true) {
		t.Fatal("eligible failure rejected")
	}
	for _, c := range [][2]bool{{false, true}, {true, false}, {false, false}} {
		if automaticRepairEligible("declared_local_check", c[0], c[1]) {
			t.Errorf("repaired with writer settled %v and a concrete target %v", c[0], c[1])
		}
	}
}

func TestRecoveryPolicyRefusesEveryLoosening(t *testing.T) {
	edit := func(old, new string) string {
		if !strings.Contains(goodRecoveryPolicy, old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(goodRecoveryPolicy, old, new, 1)
	}
	p, err := ParseRecoveryPolicy([]byte(goodRecoveryPolicy))
	if err != nil || !p.allows("declared_local_check") || p.allows("stuck") {
		t.Fatalf("the good policy: %+v %v", p, err)
	}
	for name, body := range map[string]string{
		"not json":             "policy",
		"another schema":       edit(`"schema_version": 1`, `"schema_version": 2`),
		"another name":         edit(`"bounded-v1"`, `"bounded-v2"`),
		"two repairs per work": edit(`"max_automatic_repairs_per_work": 1`, `"max_automatic_repairs_per_work": 2`),
		"no repairs":           edit(`"max_automatic_repairs_per_work": 1`, `"max_automatic_repairs_per_work": 0`),
		"escalate elsewhere":   edit(`"escalate_to": "planner"`, `"escalate_to": "nobody"`),
		"automatic replanning": edit(`"replanning": "none"`, `"replanning": "automatic"`),
		"an unknown kind":      edit(`"declared_local_check"`, `"unknown"`),
		"an infra kind":        edit(`"declared_local_check"`, `"infra"`),
		"a stuck kind":         edit(`"declared_local_check"`, `"stuck"`),
		"no kinds":             edit(`["declared_local_check", "review_changes_requested"]`, `[]`),
		"a repeated kind":      edit(`"review_changes_requested"`, `"declared_local_check"`),
		"an unknown field":     edit(`"replanning": "none"`, `"replanning": "none", "retry_forever": true`),
		"trailing data":        goodRecoveryPolicy + `{}`,
		"too large":            edit(`"name": "bounded-v1"`, `"name": "bounded-v1", "note": "`+strings.Repeat("x", maxRecoveryPolicyBytes)+`"`),
	} {
		if _, err := ParseRecoveryPolicy([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if only, err := ParseRecoveryPolicy([]byte(edit(`["declared_local_check", "review_changes_requested"]`, `["declared_local_check"]`))); err != nil || only.allows("review_changes_requested") {
		t.Errorf("a policy may narrow the allowlist: %+v %v", only, err)
	}
}

func (e *cenv) recoveryPolicyPath(body string) string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), "recovery-policy.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// startRecovery starts the run in a collaboration under bounded-v1. The run's
// tasks are bound to logical work at start, each with its one repair allowance.
// bind=false removes those bindings, as for a task nobody mapped.
func (e *cenv) startRecovery(bind bool) {
	e.t.Helper()
	e.startRecoveryWith(nil)
	if !bind {
		if _, err := e.raw().Exec(`DELETE FROM work_task_bindings`); err != nil {
			e.t.Fatal(err)
		}
	}
}

// startRecoveryWith lets a test register work in the collaboration first, as an
// earlier run of the same plan would have.
func (e *cenv) startRecoveryWith(prepare func()) {
	e.t.Helper()
	policy, err := e.c.put(ctx, `{"recovery":"bounded-v1"}`)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy,
		InputCeilings: map[string]int64{"codex": 100000}, OutputCeilings: map[string]int64{"codex": 10000}}); err != nil {
		e.t.Fatal(err)
	}
	if prepare != nil {
		prepare()
	}
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.Collaboration, r.RecoveryPolicyPath = testCollab, e.recoveryPolicyPath(goodRecoveryPolicy)
	}))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
}

func TestRecoveryStartNeedsACollaborationAndAValidPolicy(t *testing.T) {
	e := newCEnv(t)
	req := e.startRequest(func(r *StartRequest) { r.RecoveryPolicyPath = e.recoveryPolicyPath(goodRecoveryPolicy) })
	if _, err := e.c.Start(ctx, req); err == nil {
		t.Fatal("bounded recovery started outside a collaboration")
	}
	policy, _ := e.c.put(ctx, `{}`)
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy}); err != nil {
		t.Fatal(err)
	}
	req.Collaboration = testCollab
	req.RecoveryPolicyPath = e.recoveryPolicyPath(strings.Replace(goodRecoveryPolicy, `"replanning": "none"`, `"replanning": "auto"`, 1))
	if _, err := e.c.Start(ctx, req); err == nil {
		t.Fatal("a policy that replans was accepted")
	}
	req.RecoveryPolicyPath = filepath.Join(t.TempDir(), "absent.json")
	if _, err := e.c.Start(ctx, req); err == nil {
		t.Fatal("a missing policy file was accepted")
	}
	if n := e.countWhere(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("refused starts left %d runs", n)
	}
}

// Without the flag nothing changes: the configuration carries no recovery key, a
// failed check pauses and stays paused, and a reviewer's request for changes
// takes the existing repair path with no admission and no incident.
func TestRecoveryFlagAbsentPreservesBehavior(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	e.bindRecoveryWork("task-1")
	run, _ := e.st.GetRun(ctx, e.runID)
	raw, _ := e.art.ReadAll(ctx, run.ConfigHash, 1<<20)
	if strings.Contains(string(raw), "recovery") {
		t.Fatalf("a run that did not opt in has recovery in its configuration: %s", raw)
	}
	e.chk.exit = 1
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{}\n")
	for i := 0; i < 4; i++ {
		e.tick()
	}
	if st, code := e.runState(); st != "paused" || code != workflow.PauseCheckFailed {
		t.Fatalf("run %s/%s: a failed check pauses and waits for a person", st, code)
	}
	for _, q := range []string{`SELECT count(*) FROM repair_admissions`, `SELECT count(*) FROM recovery_incidents`,
		`SELECT count(*) FROM effects WHERE kind='recovery_decision'`, `SELECT count(*) FROM manual_interventions`} {
		if n := e.countWhere(q); n != 0 {
			t.Errorf("%s = %d for a run with no recovery policy", q, n)
		}
	}
	if e.attemptsOf("task-1") != 1 {
		t.Fatal("a repair was started for a failed check")
	}
}

func TestRecoveryFlagAbsentKeepsTheReviewDrivenRepair(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	e.bindRecoveryWork("task-1")
	e.passTask(1, 1)
	if e.attemptsOf("task-1") != 2 {
		t.Fatalf("%d attempts after one requested change", e.attemptsOf("task-1"))
	}
	if n := e.countWhere(`SELECT count(*) FROM repair_admissions`); n != 0 {
		t.Fatalf("the old repair path spent %d automatic admissions", n)
	}
	if e.usedRepairs() != 0 {
		t.Fatal("the old repair path consumed a cross-run allowance")
	}
}

// A recovery run binds each of its tasks to its own work and allowance, with no
// help from a person. Starting it binds nothing outside the collaboration.
func TestRecoveryStartBindsEveryTaskToItsOwnAllowance(t *testing.T) {
	e := newCEnv(t)
	e.startRecovery(true)
	tasks := e.tasks()
	works := map[string]bool{}
	for _, task := range tasks {
		w, err := e.st.TaskWork(ctx, e.runID, task.ID)
		if err != nil || w == "" {
			t.Fatalf("task %s is bound to %q (%v)", task.ID, w, err)
		}
		works[w] = true
		b, err := e.st.WorkRepairBudgets(ctx, testCollab, w)
		if err != nil || len(b) != 1 || b[0].Allowance != 1 || b[0].Used != 0 {
			t.Fatalf("task %s draws on %+v (%v)", task.ID, b, err)
		}
	}
	if len(works) != len(tasks) || e.countWhere(`SELECT count(*) FROM budget_groups`) != len(tasks) {
		t.Fatalf("%d works and %d groups for %d tasks: each task has exactly its own", len(works), e.countWhere(`SELECT count(*) FROM budget_groups`), len(tasks))
	}
}

// A replacement run of the same plan binds to the work, and the spent
// allowance, of the run before it; starting again mints nothing.
func TestRecoveryReplacementRunInheritsTheSpentAllowance(t *testing.T) {
	e := newCEnv(t)
	e.startRecoveryWith(func() {
		if err := e.st.CreateLogicalWork(ctx, state.WorkRequest{CollaborationID: testCollab, ID: recoveryWork, BudgetGroupID: recoveryGroup, Alias: "task-1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.raw().Exec(`UPDATE budget_groups SET repairs_used=1 WHERE id=?`, recoveryGroup); err != nil {
			t.Fatal(err)
		}
	})
	if w, _ := e.st.TaskWork(ctx, e.runID, "task-1"); w != recoveryWork {
		t.Fatalf("task-1 is bound to %q, not the earlier run's work", w)
	}
	if n := e.countWhere(`SELECT count(*) FROM budget_groups WHERE collaboration_id=?`, testCollab); n != 2 {
		t.Fatalf("%d groups: only task-2 needed a new one", n)
	}
	e.changesRequested(1, openFinding)
	inc := e.openIncidents()
	if len(inc) != 1 || !strings.Contains(inc[0].Reason, "no automatic repair left") || e.attemptsOf("task-1") != 1 {
		t.Fatalf("a spent allowance was not inherited: %+v, %d attempts", inc, e.attemptsOf("task-1"))
	}
}

// Work that has split into pieces is not mapped for the task: minting a fresh
// allowance would reset a spent one.
func TestRecoveryAmbiguousWorkIsLeftUnboundNotReallocated(t *testing.T) {
	e := newCEnv(t)
	e.startRecoveryWith(func() {
		if err := e.st.CreateLogicalWork(ctx, state.WorkRequest{CollaborationID: testCollab, ID: recoveryWork, BudgetGroupID: recoveryGroup, Alias: "task-1"}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SplitWork(ctx, state.SplitRequest{CollaborationID: testCollab, ParentID: recoveryWork,
			Children: []state.WorkChild{{ID: "0000000000000000000000000000d011", Alias: "task-1a"}, {ID: "0000000000000000000000000000d012", Alias: "task-1b"}}}); err != nil {
			t.Fatal(err)
		}
	})
	if w, _ := e.st.TaskWork(ctx, e.runID, "task-1"); w != "" {
		t.Fatalf("task-1 was bound to %q although its work split", w)
	}
	if n := e.countWhere(`SELECT count(*) FROM budget_groups WHERE collaboration_id=?`, testCollab); n != 2 {
		t.Fatalf("%d groups: the split work's group must not be duplicated for task-1", n)
	}
	e.changesRequested(1, openFinding)
	if inc := e.openIncidents(); len(inc) != 1 || !strings.Contains(inc[0].Reason, "bound to no logical work") {
		t.Fatalf("incidents %+v", inc)
	}
}

// Without the policy nothing is bound.
func TestRecoveryFlagAbsentBindsNoWork(t *testing.T) {
	e := newCEnv(t)
	e.startInCollaboration()
	if n := e.countWhere(`SELECT count(*) FROM logical_work`) + e.countWhere(`SELECT count(*) FROM work_task_bindings`); n != 0 {
		t.Fatalf("a run that did not opt in registered %d work rows", n)
	}
}
