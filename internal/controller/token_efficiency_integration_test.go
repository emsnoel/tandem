package controller

// Failure modes of the token-efficiency work, driven through the real
// controller, state store and mailbox. The pure predicates are covered where
// they live; these tests chain them, restart the controller between steps and
// race real SQLite writers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// restart is the same logical writer in a new process: a new controller that
// recovers the run before it ticks.
func (e *cenv) restart() {
	e.t.Helper()
	e.c = e.newController()
	if err := e.c.Recover(ctx, e.runID); err != nil {
		e.t.Fatalf("recover after restart: %v", err)
	}
}

// ownedEnv is an env whose controllers share one owner identity, so a restart
// can take its own lease back.
func ownedEnv(t *testing.T) *cenv {
	t.Helper()
	e := newCEnv(t)
	e.owner = "the-controller"
	e.c = e.newController()
	return e
}

func (e *cenv) countWhere(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.raw().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// A handback run whose review is waived is accepted exactly once, with a controller restart between
// every stage. Each model launch is admitted once, and the builder and the
// planner are both registered as counted sources.
func TestTokenEfficiencyHandbackRunIsAcceptedOnceAcrossRestarts(t *testing.T) {
	e := ownedEnv(t)
	e.startEnforcedHandback(100000)
	att := e.toReview("task-1", 1)
	e.restart() // before the review is waived
	e.waiveReview(att)
	e.restart() // before the planner has the packet
	e.ticksUntilVerificationSent(att)
	e.restart() // with the verification prompt in flight
	if e.accepted() != 0 {
		t.Fatal("the task was accepted on a waived review, before the planner verified it")
	}
	e.plannerAnswers(att, workflow.ReviewStatusPassed, "planner-conversation")
	for i := 0; i < 6; i++ {
		e.restart()
		e.tick()
	}
	if e.accepted() != 1 || e.acceptEvents() != 1 {
		t.Fatalf("accepted %d, accept events %d", e.accepted(), e.acceptEvents())
	}
	sent := e.delivered()
	if sent[workflow.EffectBuilderDispatch] != 2 || sent[effectVerifyPrompt] != 1 {
		t.Fatalf("launches %v: one verification, and one builder prompt per task", sent)
	}
	models := sent[workflow.EffectBuilderDispatch] + sent[effectReviewPrompt] + sent[effectVerifyPrompt]
	if n := e.countWhere(`SELECT count(*) FROM dispatch_admissions`); n != models {
		t.Fatalf("%d admissions for %d model launches", n, models)
	}
	if n := e.countWhere(`SELECT count(DISTINCT effect_id) FROM dispatch_admissions`); n != models {
		t.Fatalf("an effect was admitted twice: %d distinct of %d", n, models)
	}
	for _, role := range []string{"builder", "planner"} {
		if e.countWhere(`SELECT count(*) FROM expected_sources WHERE collaboration_id=? AND role=?`, enforcedCollab, role) == 0 {
			t.Errorf("no %s source is registered for counting", role)
		}
	}
	if !e.leaseOwned() {
		t.Error("the run lost its single writer lease")
	}
}

// A stop dominates a verification that is ready: the result stays staged, the
// task is not accepted, and nothing is dispatched, however often the
// controller restarts. Resuming applies it once.
func TestTokenEfficiencyStopDominatesAReadyVerification(t *testing.T) {
	e := ownedEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	e.ticksUntilVerificationSent(att)
	e.plannerAnswers(att, workflow.ReviewStatusPassed, "planner-conversation")
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	before := e.delivered()
	for i := 0; i < 4; i++ {
		e.restart()
		e.tick()
	}
	job, _ := e.jobByStage(att, "independent_verification")
	if st := e.countWhere(`SELECT count(*) FROM review_candidates WHERE job_id=? AND state='staged'`, job.ID); st != 1 {
		t.Fatalf("the verification candidate is not still staged under a stop (%d staged)", st)
	}
	if e.accepted() != 0 || e.acceptEvents() != 0 {
		t.Fatalf("a stopped run accepted %d tasks (%d events)", e.accepted(), e.acceptEvents())
	}
	after := e.delivered()
	for _, kind := range []string{workflow.EffectBuilderDispatch, effectReviewPrompt, effectVerifyPrompt, effectNotify} {
		if after[kind] != before[kind] { // the stop's own cancel is not new work
			t.Errorf("a stopped run sent %d more %s", after[kind]-before[kind], kind)
		}
	}
	if state, _ := e.runState(); state == "running" {
		t.Errorf("the run is %s after a stop", state)
	}
}

// The same verification result delivered by many writers at once is staged
// once, through the real SQLite store.
func TestTokenEfficiencyConcurrentDeliveriesOfOneResultStageOnce(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.waiveReview(att)
	prompt := e.ticksUntilVerificationSent(att)
	job, _ := e.jobByStage(att, "independent_verification")
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash,
		Status: workflow.ReviewStatusPassed, Findings: []workflow.Finding{}}
	payload := string(encode(res))
	name := "payloads/concurrent-result.json"
	if err := os.WriteFile(filepath.Join(RunDir(e.relay, e.runID), name), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Envelope{SchemaVersion: 3, EventID: fmt.Sprintf("%032x", 0xD000), RunID: e.runID, TaskID: att.TaskID, AttemptID: att.ID,
		JobID: prompt.JobID, EffectID: prompt.ID, Nonce: prompt.Nonce, Kind: "review_result", PayloadPath: name, PayloadHash: sha(payload)})

	const writers = 8
	errs := make([]error, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	controllers := make([]*Controller, writers) // built here: construction touches shared fixture state
	for i := range controllers {
		controllers[i] = e.newController()
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = controllers[i].Ingest(ctx, raw)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	if n := e.countWhere(`SELECT count(*) FROM review_candidates WHERE job_id=?`, job.ID); n != 1 {
		t.Fatalf("%d candidates staged for one result", n)
	}
	e.tr.setAgent("planner-session", runner.Observation{State: runner.ObsCompleted, ExternalID: "planner-conversation"})
	for i := 0; i < 6; i++ {
		e.tick()
	}
	if e.accepted() != 1 || e.acceptEvents() != 1 {
		t.Fatalf("accepted %d, accept events %d", e.accepted(), e.acceptEvents())
	}
}

// A run that never opted in behaves as it did before: no verification turn,
// one review prompt per task, protocol 2 in its pinned configuration.
func TestTokenEfficiencyOldProtocolRunIsUnchanged(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.passTask(1, 0)
	e.passTask(2, 0)
	if state, _ := e.runState(); state != "finished" {
		t.Fatalf("run %s", state)
	}
	sent := e.delivered()
	if sent[effectVerifyPrompt] != 0 {
		t.Fatalf("an old run asked its planner for %d verifications", sent[effectVerifyPrompt])
	}
	if sent[effectReviewPrompt] != 2 {
		t.Fatalf("%d review prompts for two tasks", sent[effectReviewPrompt])
	}
	if p, err := RunReviewProtocol(ctx, e.st, e.c.d.Artifacts, e.runID); err != nil || p != 2 {
		t.Fatalf("pinned protocol %d, %v", p, err)
	}
	if n := e.countWhere(`SELECT count(*) FROM review_candidates`); n != 0 {
		t.Fatalf("an old run staged %d direct review results", n)
	}
	turns, err := e.c.plannerTurns(ctx, e.runID)
	if err != nil || turns.Verification != 0 || turns.Coordination != 0 {
		t.Fatalf("planner turns %+v, %v", turns, err)
	}
}
