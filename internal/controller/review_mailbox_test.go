package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// reviewEnv is the mailbox fixture plus a reviewer job and the review prompt
// effect that asks for it, registered for review protocol 3.
type reviewEnv struct {
	*mailboxEnv
	rjob, reff, rnonce string
	n                  int
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	m := newMailboxEnv(t)
	m.deps.ReviewProtocol = 3
	r := &reviewEnv{mailboxEnv: m, rjob: hex32(401), reff: hex32(402), rnonce: hex32(403)}
	h, err := m.art.Put(ctx, strings.NewReader("reviewer fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.st.AdoptArtifact(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := m.st.InsertJob(ctx, workflow.Job{ID: r.rjob, AttemptID: m.attempt, Kind: "reviewer", State: "running", InputHash: h,
		PatchHash: h, TreeHash: h, ConfigHash: h, RunnerKind: paneRunnerKind}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(m.stRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO effects(id,run_id,attempt_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms)
		VALUES(?,?,?,?, 'review_prompt',1,?,?, 'delivering',1,1,1)`, r.reff, m.run, m.attempt, hex32(212), h, r.rnonce); err != nil {
		t.Fatal(err)
	}
	if err := m.st.RegisterReviewStage(ctx, r.rjob, state.StageBuilderReview); err != nil {
		t.Fatal(err)
	}
	if err := m.st.BindEffectJob(ctx, r.reff, r.rjob); err != nil {
		t.Fatal(err)
	}
	return r
}

// msg writes a payload and returns a version 3 envelope for it.
func (r *reviewEnv) msg(t *testing.T, kind, payload string, mutate func(*Envelope)) []byte {
	t.Helper()
	r.n++
	name := fmt.Sprintf("payloads/%s-%d.json", kind, r.n)
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Envelope{SchemaVersion: 3, EventID: hex32(500 + r.n), RunID: r.run, TaskID: "receipt-store", AttemptID: r.attempt, JobID: r.rjob,
		EffectID: r.reff, Nonce: r.rnonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)}
	if mutate != nil {
		mutate(&env)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (r *reviewEnv) effectState(t *testing.T) string {
	t.Helper()
	eff, err := r.st.GetEffect(ctx, r.reff)
	if err != nil {
		t.Fatal(err)
	}
	return eff.State
}

func (r *reviewEnv) candidate(t *testing.T) (state.ReviewCandidate, bool) {
	t.Helper()
	c, found, err := r.st.ReviewCandidateFor(ctx, r.rjob)
	if err != nil {
		t.Fatal(err)
	}
	return c, found
}

const resultJSON = "{\"schema_version\":1,\"status\":\"passed\",\"findings\":[]}\n"

func TestReviewMailboxKindsByProtocolVersion(t *testing.T) {
	cases := []struct {
		version   int
		kind      string
		permitted bool
	}{
		{2, "review_result", false},
		{2, "review_started", false},
		{3, "review_started", true},
		{3, "review_result", true},
		{3, workflow.EventBuilderSubmitted, false},
	}
	for _, tc := range cases {
		r := newReviewEnv(t)
		payload := resultJSON
		if tc.kind == "review_started" {
			payload = "{}\n"
		}
		_, err := IngestMailbox(ctx, r.deps, r.msg(t, tc.kind, payload, func(en *Envelope) { en.SchemaVersion = tc.version }))
		var rej *RejectError
		refusedByKind := errors.As(err, &rej) && (rej.Code == "unsupported_kind" || rej.Code == "unsupported_protocol")
		if tc.permitted == refusedByKind || (tc.permitted && err != nil) {
			t.Errorf("version %d %s: permitted=%v, got %v", tc.version, tc.kind, tc.permitted, err)
		}
	}
}

func TestReviewMailboxSubmissionStagesACandidateAndAcceptsNothing(t *testing.T) {
	r := newReviewEnv(t)
	eventsBefore, effectsBefore, implBefore, _ := r.counts(t)
	rec, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil))
	if err != nil || rec.Duplicate {
		t.Fatalf("result: %+v %v", rec, err)
	}
	cand, found := r.candidate(t)
	if !found || cand.State != "staged" || cand.Stage != state.StageBuilderReview || cand.JobID != r.rjob || cand.EffectID != r.reff {
		t.Fatalf("candidate %+v found %v", cand, found)
	}
	// The candidate is the reviewer's exact bytes, as an artifact.
	if raw, err := r.art.ReadAll(ctx, cand.PayloadHash, 1<<20); err != nil || string(raw) != resultJSON {
		t.Fatalf("stored bytes %q %v", raw, err)
	}
	// A submission changes no task: no event, no new effect, no review state.
	events, effects, impl, _ := r.counts(t)
	att, _ := r.st.GetAttempt(ctx, r.attempt)
	if events != eventsBefore || effects != effectsBefore || impl != implBefore || att.Review != workflow.ReviewPending {
		t.Fatalf("a submission moved the run: events %d->%d effects %d->%d implementation %s review %s", eventsBefore, events, effectsBefore, effects, impl, att.Review)
	}
	if r.effectState(t) != "acknowledged" {
		t.Fatalf("the prompt effect is %s: the nonce proves it was read", r.effectState(t))
	}

	// The same bytes again are the same receipt, under the same or another event.
	again, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, func(en *Envelope) { en.EventID = cand.EventID }))
	if err != nil || !again.Duplicate || again.EventID != cand.EventID {
		t.Fatalf("a replay: %+v %v", again, err)
	}
	other, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil))
	if err != nil || !other.Duplicate {
		t.Fatalf("the same bytes under another event: %+v %v", other, err)
	}
	// Different bytes for the same job conflict, never replace.
	_, err = IngestMailbox(ctx, r.deps, r.msg(t, "review_result", "{\"schema_version\":1,\"status\":\"failed\",\"findings\":[]}\n", nil))
	if got := code(t, err); got != "conflicting_candidate" {
		t.Fatalf("conflicting bytes: %v", err)
	}
	if kept, _ := r.candidate(t); kept.PayloadHash != cand.PayloadHash {
		t.Fatal("a conflicting result replaced the candidate")
	}
	// Different bytes under the first event id conflict too.
	_, err = IngestMailbox(ctx, r.deps, r.msg(t, "review_result", "{\"schema_version\":1,\"status\":\"failed\"}\n", func(en *Envelope) { en.EventID = cand.EventID }))
	if got := code(t, err); got != "conflicting_candidate" {
		t.Fatalf("same event, other bytes: %v", err)
	}
}

func TestReviewMailboxBindingsAreChecked(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Envelope)
		want   string
	}{
		"wrong nonce":        {func(en *Envelope) { en.Nonce = hex32(999) }, "wrong_nonce"},
		"wrong job":          {func(en *Envelope) { en.JobID = hex32(998) }, "unknown_job"},
		"the builder's job":  {func(en *Envelope) { en.JobID = hex32(203) }, "unknown_job"},
		"unknown attempt":    {func(en *Envelope) { en.AttemptID = hex32(997) }, "stale_attempt"},
		"wrong task":         {func(en *Envelope) { en.TaskID = "other-task" }, "wrong_task"},
		"unknown effect":     {func(en *Envelope) { en.EffectID = hex32(996) }, "unknown_effect"},
		"the dispatch":       {func(en *Envelope) { en.EffectID = hex32(204); en.Nonce = hex32(206) }, "unknown_effect"},
		"another run":        {func(en *Envelope) { en.RunID = hex32(995) }, "wrong_run"},
		"payload hash":       {func(en *Envelope) { en.PayloadHash = sha("something else") }, "payload_hash_mismatch"},
		"absent payload":     {func(en *Envelope) { en.PayloadPath = "payloads/nothing.json"; en.PayloadHash = sha("") }, "invalid_payload_path"},
		"payload with a ..":  {func(en *Envelope) { en.PayloadPath = "payloads/../q/x" }, "invalid_payload_path"},
		"submits as builder": {func(en *Envelope) { en.Kind = workflow.EventBuilderSubmitted }, "unsupported_kind"},
	}
	for name, tc := range cases {
		r := newReviewEnv(t)
		_, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, tc.mutate))
		if got := code(t, err); got != tc.want {
			t.Errorf("%s: code %s (%v), want %s", name, got, err, tc.want)
		}
		if _, found := r.candidate(t); found {
			t.Errorf("%s: a rejected message left a candidate", name)
		}
		if r.effectState(t) != "delivering" {
			t.Errorf("%s: a rejected message acknowledged the prompt", name)
		}
	}
}

// The effect must ask for the job the message names. A prompt effect that was
// never bound, or was bound to another reviewer job, cannot carry a result.
func TestReviewMailboxEffectMustBeBoundToTheNamedJob(t *testing.T) {
	r := newReviewEnv(t)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(r.stRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	defer db.Close()
	if _, err := db.Exec(`UPDATE effects SET job_id=NULL WHERE id=?`, r.reff); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "unknown_job" {
		t.Fatalf("a prompt effect bound to no job: %v", err)
	}
	other := hex32(411)
	h, _ := r.art.Put(ctx, strings.NewReader("second reviewer"))
	r.st.AdoptArtifact(ctx, h)
	if err := r.st.InsertJob(ctx, workflow.Job{ID: other, AttemptID: r.attempt, Kind: "reviewer", State: "running", InputHash: h, PatchHash: h,
		TreeHash: h, ConfigHash: h, RunnerKind: paneRunnerKind}); err != nil {
		t.Fatal(err)
	}
	if err := r.st.RegisterReviewStage(ctx, other, state.StageBuilderReview); err != nil {
		t.Fatal(err)
	}
	if err := r.st.BindEffectJob(ctx, r.reff, other); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "unknown_job" {
		t.Fatalf("a prompt effect bound to another reviewer job: %v", err)
	}
	if err := r.st.BindEffectJob(ctx, r.reff, r.rjob); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("rebinding an effect to another job: %v", err)
	}
	if _, found := r.candidate(t); found {
		t.Fatal("a mis-bound message left a candidate")
	}
}

// A stage is asked for by its own prompt. A builder review prompt cannot carry
// an independent verification, and a verification prompt cannot carry a
// builder review, even when the job and effect are otherwise bound together.
func TestReviewMailboxStageMustMatchItsPromptKind(t *testing.T) {
	r := newReviewEnv(t)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(r.stRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	defer db.Close()
	// The job serves verification, the effect is a builder review prompt.
	db.Exec(`DELETE FROM review_stages WHERE job_id=?`, r.rjob)
	if err := r.st.RegisterReviewStage(ctx, r.rjob, state.StageIndependentVerification); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "unknown_effect" {
		t.Fatalf("a verification carried by a builder review prompt: %v", err)
	}
	// The job serves the builder review, the effect is a verification prompt.
	db.Exec(`DELETE FROM review_stages WHERE job_id=?`, r.rjob)
	if err := r.st.RegisterReviewStage(ctx, r.rjob, state.StageBuilderReview); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE effects SET kind='verify_prompt' WHERE id=?`, r.reff); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "unknown_effect" {
		t.Fatalf("a builder review carried by a verification prompt: %v", err)
	}
	if _, found := r.candidate(t); found {
		t.Fatal("a mismatched message left a candidate")
	}
}

func TestReviewMailboxStaleAttemptAndUnregisteredStageAreRejected(t *testing.T) {
	r := newReviewEnv(t)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(r.stRoot, "state.sqlite")+"?_pragma=busy_timeout(5000)")
	defer db.Close()
	db.Exec(`UPDATE attempts SET is_current=0 WHERE id=?`, r.attempt)
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "stale_attempt" {
		t.Fatalf("a superseded attempt: %v", err)
	}
	db.Exec(`UPDATE attempts SET is_current=1 WHERE id=?`, r.attempt)
	db.Exec(`DELETE FROM review_stages WHERE job_id=?`, r.rjob)
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "unregistered_reviewer" {
		t.Fatalf("a reviewer with no registered stage: %v", err)
	}
	// A message cannot name its own stage: there is no field for it.
	if _, err := ParseEnvelope(r.msg(t, "review_result", resultJSON, nil)); err != nil {
		t.Fatal(err)
	}
	withStage := strings.Replace(string(r.msg(t, "review_result", resultJSON, nil)), `"kind"`, `"stage":"independent_verification","kind"`, 1)
	if _, err := ParseEnvelope([]byte(withStage)); code(t, err) != "malformed_envelope" {
		t.Fatalf("a message naming its own stage: %v", err)
	}
}

func TestReviewMailboxRunMustHaveOptedIn(t *testing.T) {
	r := newReviewEnv(t)
	for _, protocol := range []int{0, 2} {
		r.deps.ReviewProtocol = protocol
		if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); code(t, err) != "unsupported_protocol" {
			t.Errorf("protocol %d: %v", protocol, err)
		}
	}
	if _, found := r.candidate(t); found {
		t.Fatal("a run that did not opt in staged a candidate")
	}
}

func TestReviewMailboxStartAndResultInEitherOrder(t *testing.T) {
	for _, startFirst := range []bool{true, false} {
		r := newReviewEnv(t)
		r.deps.ReviewDeadline = 90 * time.Second
		start := func() {
			rec, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_started", "{\"session_id\":\"reviewer-1\"}\n", nil))
			if err != nil || rec.Duplicate {
				t.Fatalf("start (startFirst=%v): %+v %v", startFirst, rec, err)
			}
		}
		result := func() {
			if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); err != nil {
				t.Fatalf("result (startFirst=%v): %v", startFirst, err)
			}
		}
		if startFirst {
			start()
			if _, found := r.candidate(t); found || r.effectState(t) != "acknowledged" {
				t.Fatal("a start alone must acknowledge delivery and stage nothing")
			}
			result()
		} else {
			result()
			start()
		}
		got, found, err := r.st.ReviewStartFor(ctx, r.rjob)
		if err != nil || !found || got.SessionID != "reviewer-1" || got.DeadlineMS-got.StartedMS != 90_000 {
			t.Fatalf("start record %+v %v %v", got, found, err)
		}
		if _, found := r.candidate(t); !found || r.effectState(t) != "acknowledged" {
			t.Fatalf("startFirst=%v: candidate staged %v, prompt %s", startFirst, found, r.effectState(t))
		}
	}
}

func TestReviewMailboxStartIsCheckedAgainstTheTransport(t *testing.T) {
	r := newReviewEnv(t)
	r.deps.ObserveSession = func(_ context.Context, id string) string {
		if id == r.reff {
			return "session-seen-by-the-transport"
		}
		return ""
	}
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_started", "{\"session_id\":\"reviewer-claims\"}\n", nil)); code(t, err) != "session_mismatch" {
		t.Fatalf("a session the transport did not see: %v", err)
	}
	if r.effectState(t) != "delivering" {
		t.Fatal("a mismatched start acknowledged the prompt")
	}
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_started", "{}\n", nil)); err != nil {
		t.Fatalf("a start that names no session takes the transport's: %v", err)
	}
	got, _, _ := r.st.ReviewStartFor(ctx, r.rjob)
	if got.ObservedSession != "session-seen-by-the-transport" || got.SessionID != "" {
		t.Fatalf("start %+v", got)
	}
	// A second start from another session for the same job conflicts.
	r.deps.ObserveSession = nil
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_started", "{\"session_id\":\"a\"}\n", nil)); err != nil {
		t.Fatalf("a repeated start: %v", err)
	}
	for name, payload := range map[string]string{"not json": "session", "unknown field": "{\"pid\":1}\n", "bad session": "{\"session_id\":\"a b/c\"}\n",
		"empty": "", "array": "[]\n"} {
		if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_started", payload, nil)); code(t, err) != "malformed_start" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestReviewMailboxMalformedAndOverlargeResultsStageNothing(t *testing.T) {
	big := "{\"findings\":\"" + strings.Repeat("x", MaxPayloadBytes) + "\"}"
	for name, tc := range map[string]struct{ payload, want string }{
		"not json":      {"the review is fine", "malformed_result"},
		"empty":         {"", "malformed_result"},
		"an array":      {"[1]\n", "malformed_result"},
		"null":          {"null\n", "malformed_result"},
		"two objects":   {"{}\n{}\n", "malformed_result"},
		"a truncated":   {"{\"status\":\"passed\"", "malformed_result"},
		"over the size": {big, "payload_too_large"},
	} {
		r := newReviewEnv(t)
		if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", tc.payload, nil)); code(t, err) != tc.want {
			t.Errorf("%s: %v, want %s", name, err, tc.want)
		}
		if _, found := r.candidate(t); found {
			t.Errorf("%s: staged a candidate", name)
		}
	}
}

func TestReviewMailboxCrashAfterReceiptReplaysIdempotently(t *testing.T) {
	r := newReviewEnv(t)
	claimed := filepath.Join(r.dir, "q", "result.json.taken")
	if err := os.WriteFile(claimed, r.msg(t, "review_result", resultJSON, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	r.deps.afterReceipt = func() error { return errors.New("killed after the receipt") }
	if _, err := ProcessClaimed(ctx, r.deps, claimed); err == nil {
		t.Fatal("the failpoint did not fire")
	}
	if _, err := os.Stat(claimed); err != nil {
		t.Fatal("the claimed file must stay for the replay")
	}
	r.deps.afterReceipt = nil
	res, err := ProcessClaimed(ctx, r.deps, claimed)
	if err != nil || res.Disposition != Duplicate {
		t.Fatalf("replay: %+v %v", res, err)
	}
	if _, err := os.Stat(claimed); !os.IsNotExist(err) {
		t.Fatal("the replay left the claimed file")
	}
	var n int
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(r.stRoot, "state.sqlite"))
	defer db.Close()
	db.QueryRow(`SELECT count(*) FROM review_candidates`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d candidates after the replay", n)
	}
}

func TestReviewMailboxInvalidEnvelopeIsQuarantined(t *testing.T) {
	r := newReviewEnv(t)
	claimed := filepath.Join(r.dir, "q", "bad.json.taken")
	if err := os.WriteFile(claimed, r.msg(t, "review_result", resultJSON, func(en *Envelope) { en.Nonce = hex32(999) }), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ProcessClaimed(ctx, r.deps, claimed)
	if err != nil || res.Disposition != Quarantined || res.Reason != "wrong_nonce" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "quarantine", "bad.json.taken.reason")); err != nil {
		t.Fatalf("no reason beside the quarantined message: %v", err)
	}
}

func TestReviewMailboxStagedBytesSurviveGC(t *testing.T) {
	r := newReviewEnv(t)
	if _, err := IngestMailbox(ctx, r.deps, r.msg(t, "review_result", resultJSON, nil)); err != nil {
		t.Fatal(err)
	}
	cand, _ := r.candidate(t)
	lone, err := r.art.Put(ctx, strings.NewReader("nothing refers to this"))
	if err != nil {
		t.Fatal(err)
	}
	r.st.AdoptArtifact(ctx, lone)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(r.stRoot, "state.sqlite")+"?_pragma=busy_timeout(5000)")
	db.Exec(`UPDATE runs SET state='finished'`)
	db.Close()
	rep, err := r.st.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	deleted := strings.Join(rep.Deleted, " ")
	if strings.Contains(deleted, cand.PayloadHash) {
		t.Fatalf("GC deleted the staged result: %v", rep.Deleted)
	}
	if !strings.Contains(deleted, lone) {
		t.Fatalf("GC kept an unreferenced artifact, so this proves nothing: %v", rep.Deleted)
	}
	if _, err := r.art.ReadAll(ctx, cand.PayloadHash, 1<<20); err != nil {
		t.Fatalf("the staged bytes are gone: %v", err)
	}
}

// The controller registers what a reviewer's messages are checked against, and
// only for a run that opted in. A staged result accepts nothing, however many
// ticks follow it.
func TestReviewMailboxThroughTheController(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	prompts := e.effects("review_prompt")
	if len(prompts) != 1 {
		t.Fatalf("%d review prompts", len(prompts))
	}
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(jobs) != 1 || prompts[0].JobID != jobs[0].ID {
		t.Fatalf("the prompt effect is bound to %q, the reviewer job is %v", prompts[0].JobID, jobs)
	}
	if stage, ok, _ := e.st.ReviewStage(ctx, jobs[0].ID); !ok || stage != state.StageBuilderReview {
		t.Fatalf("stage %q %v", stage, ok)
	}

	e.sendReview(att, prompts[0], "review_started", "{\"session_id\":\"reviewer-session\"}\n")
	e.sendReview(att, prompts[0], "review_result", `{"schema_version":1,"status":"failed","findings":[]}`)
	if cand, found, _ := e.st.ReviewCandidateFor(ctx, jobs[0].ID); !found || cand.State != "staged" {
		t.Fatalf("candidate %+v %v", cand, found)
	}
	e.reviewerFinished(att, "reviewer-session")
	for i := 0; i < 5; i++ {
		e.tick()
	}
	if got := e.reviewState(att); got == workflow.ReviewPassed {
		t.Fatalf("review state %s: a staged result must not accept the task", got)
	}
	for _, task := range e.tasks() {
		if task.AcceptedAttemptID != "" {
			t.Fatalf("task %s accepted on submission alone", task.ID)
		}
	}
}

func TestReviewMailboxRunWithoutOptInRegistersNothing(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	prompts := e.effects("review_prompt")
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(prompts) != 1 || len(jobs) != 1 || prompts[0].JobID != "" {
		t.Fatalf("a run without handback bound its prompt: %+v", prompts)
	}
	if _, ok, _ := e.st.ReviewStage(ctx, jobs[0].ID); ok {
		t.Fatal("a run without handback registered a review stage")
	}
	run, _ := e.st.GetRun(ctx, e.runID)
	cfg, err := e.c.config(ctx, run)
	if err != nil || strings.Contains(string(encode(cfg)), "review_") {
		t.Fatalf("its configuration changed: %s %v", encode(cfg), err)
	}
}

func TestReviewMailboxStartOptionsAreValidated(t *testing.T) {
	for name, req := range map[string]StartRequest{
		"mailbox without protocol 3":      {ReviewDelivery: "mailbox", Collaboration: testCollab, AcceptancePolicyPath: "p.json"},
		"mailbox without a collaboration": {ReviewDelivery: "mailbox", ReviewProtocol: 3, AcceptancePolicyPath: "p.json"},
		"mailbox without a policy":        {ReviewDelivery: "mailbox", ReviewProtocol: 3, Collaboration: testCollab},
		"a policy without mailbox":        {AcceptancePolicyPath: "p.json"},
		"protocol 3 without mailbox":      {ReviewProtocol: 3, Collaboration: testCollab},
		"an unknown delivery":             {ReviewDelivery: "carrier-pigeon"},
		"protocol 4":                      {ReviewProtocol: 4},
	} {
		if err := validateReviewOptions(req); !errors.Is(err, workflow.ErrInvalidTransition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]StartRequest{
		"nothing":      {},
		"explicit v2":  {ReviewProtocol: 2, ReviewDelivery: "manual"},
		"opted in":     {ReviewProtocol: 3, ReviewDelivery: "mailbox", Collaboration: testCollab, AcceptancePolicyPath: "p.json"},
		"manual alone": {ReviewDelivery: "manual"},
	} {
		if err := validateReviewOptions(req); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// startReviewHandback starts the env's run opted in to direct review handback.
func (e *cenv) startReviewHandback() {
	e.t.Helper()
	policy, err := e.c.put(ctx, `{"review":"handback"}`)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy}); err != nil {
		e.t.Fatal(err)
	}
	e.registerPlanner(testCollab)
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.Collaboration, r.ReviewProtocol, r.ReviewDelivery, r.AcceptancePolicyPath = testCollab, 3, "mailbox", e.policyPath()
	}))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
}

// registerPlanner registers the collaboration's planner session, which the
// verification turn is counted with.
func (e *cenv) registerPlanner(collab string) {
	e.t.Helper()
	if err := e.st.RegisterSource(ctx, state.SourceRequest{ID: derive("planner-source", collab), CollaborationID: collab, Provider: "claude",
		Role: "planner", SessionID: "planner-claude-session"}); err != nil {
		e.t.Fatal(err)
	}
}

// sendReview plays the reviewer: it writes a payload and a version 3 envelope
// bound to the review prompt it was sent, and ingests it.
func (e *cenv) sendReview(att workflow.Attempt, prompt workflow.Effect, kind, payload string) {
	e.t.Helper()
	if err := e.sendReviewError(att, prompt, kind, payload); err != nil {
		e.t.Fatalf("%s: %v", kind, err)
	}
}

func (e *cenv) sendReviewError(att workflow.Attempt, prompt workflow.Effect, kind, payload string) error {
	e.t.Helper()
	e.sendN++
	name := fmt.Sprintf("payloads/%s-%d.json", kind, e.sendN)
	dir := RunDir(e.relay, e.runID)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
		e.t.Fatal(err)
	}
	env := Envelope{SchemaVersion: 3, EventID: fmt.Sprintf("%032x", 0xC000+e.sendN), RunID: e.runID, TaskID: att.TaskID, AttemptID: att.ID,
		JobID: prompt.JobID, EffectID: prompt.ID, Nonce: prompt.Nonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)}
	raw, _ := json.Marshal(env)
	_, err := e.c.Ingest(ctx, raw)
	return err
}
