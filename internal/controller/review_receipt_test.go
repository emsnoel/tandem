package controller

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// toReview takes the task's current attempt to the point where its review prompt
// has been sent: started, submitted, checked.
func (e *cenv) toReview(taskID string, n int) workflow.Attempt {
	e.t.Helper()
	e.tick()
	att := e.current(taskID)
	e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
	e.work(n)
	e.settle(att)
	e.mustSend(taskID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	return att
}

// resultFor is a review result bound to the attempt's open reviewer job.
func (e *cenv) resultFor(att workflow.Attempt, status string, findings ...workflow.Finding) workflow.ReviewResult {
	e.t.Helper()
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil || len(jobs) == 0 {
		e.t.Fatalf("reviewer jobs %v %v", jobs, err)
	}
	if findings == nil {
		findings = []workflow.Finding{}
	}
	return workflow.ReviewResult{SchemaVersion: 1, JobID: jobs[len(jobs)-1].ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash, Status: status, Findings: findings}
}

func (e *cenv) reviewState(att workflow.Attempt) workflow.ReviewState {
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	return cur.Review
}

func TestReviewPassedEmptyFindingsAccepted(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	e.reviewerFinished(att, "reviewer-session")
	res := e.resultFor(att, workflow.ReviewStatusPassed)
	// The reviewer's own bytes, not a re-encoding of them.
	raw, _ := json.MarshalIndent(res, "", "      ")
	raw = append(raw, "\n\n"...)
	if _, err := e.c.RecordReviewRaw(ctx, e.runID, raw); err != nil {
		t.Fatal(err)
	}
	if got := e.reviewState(att); got != workflow.ReviewPassed {
		t.Fatalf("review is %s", got)
	}
	var stored string
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	for h, role := range mustArtifacts(t, e, att) {
		if role == "review result" {
			b, _ := e.art.ReadAll(ctx, h, 1<<20)
			stored = string(b)
		}
	}
	if stored != string(raw) || len(jobs) != 1 {
		t.Errorf("the persisted result is not the submitted bytes:\n%q\n%q", stored, raw)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Error("a passed review with a passed check did not accept the task")
	}
}

func mustArtifacts(t *testing.T, e *cenv, att workflow.Attempt) map[string]string {
	t.Helper()
	m, err := e.st.AttemptArtifacts(ctx, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReviewNeedsTheReviewerToHaveFinished(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	res := e.resultFor(att, workflow.ReviewStatusPassed)
	for name, obs := range map[string]runner.Observation{
		"still working":  {State: runner.ObsRunning},
		"unknown":        {State: runner.ObsUnknown},
		"stopped":        {State: runner.ObsStopped},
		"nothing at all": {},
	} {
		e.reviewerObserved(att, obs)
		if _, err := e.c.RecordReview(ctx, e.runID, res); !errors.Is(err, workflow.ErrIncompleteEvidence) {
			t.Errorf("%s: %v", name, err)
		}
		if e.reviewState(att) == workflow.ReviewPassed {
			t.Fatalf("%s: a review of an unfinished reviewer passed", name)
		}
	}
	if n := len(mustJobsResults(t, e, att)); n != 0 {
		t.Errorf("%d results were stored for a reviewer that had not finished", n)
	}
	e.reviewerFinished(att, "s")
	if _, err := e.c.RecordReview(ctx, e.runID, res); err != nil {
		t.Fatalf("once it finished: %v", err)
	}
}

func mustJobsResults(t *testing.T, e *cenv, att workflow.Attempt) []string {
	t.Helper()
	h, found, err := e.st.LatestReviewResult(ctx, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return nil
	}
	return []string{h}
}

func TestReviewStaleTreeRejected(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	// The tree changed after the work the reviewer was shown.
	write(t, e.root, "sneaked-in.txt", "added after the review input was fixed\n")
	e.reviewerFinished(att, "s")
	res := e.resultFor(att, workflow.ReviewStatusPassed)
	_, err := e.c.RecordReview(ctx, e.runID, res)
	if !errors.Is(err, workflow.ErrIncompleteEvidence) || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("%v", err)
	}
	if got := e.reviewState(att); got == workflow.ReviewPassed {
		t.Fatalf("a result for the previous tree passed: %s", got)
	}
	if st, code := e.runState(); st != "paused" || code != "source_mutated" {
		t.Errorf("run is %s/%s", st, code)
	}
	if h, found, _ := e.st.LatestReviewResult(ctx, att.ID); found {
		t.Errorf("the stale result %s was recorded", h)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Error("accepted")
	}
	// The job's input was never edited to match.
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	if jobs[0].TreeHash != cur.EndSnapshotHash {
		t.Error("the reviewer job's tree was rewritten")
	}
}

func TestAcceptanceChecksTheTreeStillMatchesWhatWasReviewed(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	// A valid review is on record; then the tree moves before the task is accepted.
	write(t, e.root, "late.txt", "edited after the review\n")
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Fatal("a task was accepted although its tree is no longer the one reviewed")
	}
	if st, code := e.runState(); st != "paused" || code != "source_mutated" {
		t.Errorf("run is %s/%s", st, code)
	}
}

func TestReviewExitWithoutResultNotSuccess(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	// The reviewer's turn ended and it exited cleanly, but it never handed back a result.
	e.reviewerFinished(att, "reviewer-session")
	for i := 0; i < 3; i++ {
		e.tick()
	}
	if got := e.reviewState(att); got == workflow.ReviewPassed {
		t.Fatalf("an exit with no result passed: %s", got)
	}
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Fatal("accepted without a review")
	}
	// Waiting does not turn silence into success: the prompt goes unanswered
	// and the run stops for a person.
	e.clk.advance(state.AckWindow + time.Minute)
	e.tick()
	if st, code := e.runState(); st != "paused" || code != "review_unknown" {
		t.Errorf("run is %s/%s", st, code)
	}
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Error("accepted")
	}
}

func TestReviewRawResultsAreStrictAndBoundToTheJob(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	e.reviewerFinished(att, "s")
	good := e.resultFor(att, workflow.ReviewStatusPassed)
	enc, _ := json.Marshal(good)
	for name, raw := range map[string]string{
		"not json":       "LGTM",
		"empty":          "",
		"two objects":    string(enc) + string(enc),
		"trailing prose": string(enc) + "\nlooks fine",
		"unknown field":  strings.Replace(string(enc), `"findings":[]`, `"findings":[],"verdict":"fine"`, 1),
		"too large":      strings.Repeat(" ", (1<<20)+1),
	} {
		if _, err := e.c.RecordReviewRaw(ctx, e.runID, []byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	wrong := good
	wrong.AttemptID = strings.Repeat("0", 32)
	if _, err := e.c.RecordReview(ctx, e.runID, wrong); err == nil {
		t.Error("a result for another attempt")
	}
	wrong = good
	wrong.PatchHash = strings.Repeat("0", 64)
	if _, err := e.c.RecordReview(ctx, e.runID, wrong); err == nil {
		t.Error("a result for another patch")
	}
	passedHigh := e.resultFor(att, workflow.ReviewStatusPassed, workflow.Finding{Severity: "high", Path: "a.go", Disposition: "open", Message: "bad"})
	if _, err := e.c.RecordReview(ctx, e.runID, passedHigh); err == nil {
		t.Error("a pass with an open high finding")
	}
	if e.reviewState(att) != workflow.ReviewPending && e.reviewState(att) != workflow.ReviewRunning {
		t.Errorf("a refused result moved the review to %s", e.reviewState(att))
	}
	if _, found, _ := e.st.LatestReviewResult(ctx, att.ID); found {
		t.Error("a refused result was recorded")
	}
}

func TestReviewerIdentityIsRecordedWithItsLimits(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "copilot", "opencode"} {
		for _, session := range []string{"sess-9", ""} {
			e := newCEnv(t)
			run, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ReviewerKind, r.BuilderSession, r.PlannerSession = kind, "b", "p" }))
			if err != nil {
				t.Fatal(err)
			}
			e.runID = run
			att := e.toReview("task-1", 1)
			e.reviewerFinished(att, session)
			if _, err := e.c.RecordReview(ctx, e.runID, e.resultFor(att, workflow.ReviewStatusPassed)); err != nil {
				t.Fatalf("%s/%q: %v", kind, session, err)
			}
			var doc struct {
				Kind, Mode, Session, Assurance, Note string
			}
			found := false
			for h, role := range mustArtifacts(t, e, att) {
				if role == "review identity" {
					raw, _ := e.art.ReadAll(ctx, h, 1<<20)
					if err := json.Unmarshal(raw, &doc); err != nil {
						t.Fatal(err)
					}
					found = true
				}
			}
			if !found || doc.Kind != kind || doc.Mode != "pane" || doc.Session != session {
				t.Errorf("%s/%q: identity %+v (found %v)", kind, session, doc, found)
			}
			want := "session_observed"
			if session == "" {
				want = "session_unavailable"
			}
			if doc.Assurance != want || !strings.Contains(doc.Note, "not provider-attested") {
				t.Errorf("%s/%q: assurance %q, note %q", kind, session, doc.Assurance, doc.Note)
			}
			if e.reviewState(att) != workflow.ReviewPassed {
				t.Errorf("%s/%q: a pane that reports no session still gets its review recorded, with the gap stated: %s", kind, session, e.reviewState(att))
			}
		}
	}
}

func TestReviewResultWithoutADeliveredPromptIsRefused(t *testing.T) {
	e := newCEnv(t)
	// The review prompt never reaches a reviewer: the transport refuses it.
	e.tr.sendErr = func(d runner.Delivery) error {
		if d.Kind == "review_prompt" {
			return runner.NotSent(errors.New("the pane is not ready"))
		}
		return nil
	}
	e.start()
	att := e.toReview("task-1", 1)
	prompts := e.effects("review_prompt")
	if len(prompts) != 1 || prompts[0].State == "delivering" || prompts[0].State == "acknowledged" {
		t.Fatalf("fixture: %+v", prompts)
	}
	e.tr.set(prompts[0].ID, runner.Observation{State: runner.ObsCompleted, ExternalID: "s"})
	// A result that somehow names the job is still not a review nobody was asked for.
	res := e.resultFor(att, workflow.ReviewStatusPassed)
	if _, err := e.c.RecordReview(ctx, e.runID, res); !errors.Is(err, workflow.ErrIncompleteEvidence) || !strings.Contains(err.Error(), "no review prompt was delivered") {
		t.Fatalf("%v", err)
	}
	if e.reviewState(att) == workflow.ReviewPassed {
		t.Error("passed")
	}
}
