package main

import (
	"context"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

func TestDurableStatusExplainsReviewRejection(t *testing.T) {
	d := durableRepo(t)
	open, err := openDurable(d.root, d.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer open.close()
	ctx := context.Background()
	attempts, err := open.st.TaskAttempts(ctx, d.run, "receipt-store")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %+v %v", attempts, err)
	}
	hash := attempts[0].PacketHash
	if err := open.st.InsertJob(ctx, workflow.Job{ID: d.job, AttemptID: d.attempt, Kind: "reviewer", State: "running", InputHash: hash, ConfigHash: hash, PatchHash: hash, TreeHash: hash, RunnerKind: "pane"}); err != nil {
		t.Fatal(err)
	}
	if err := open.st.SetRunPausedFor(ctx, d.run, "review_unknown", d.job, d.effect); err != nil {
		t.Fatal(err)
	}
	before, err := durableStatus(open)
	if err != nil || before.PauseReason != fixedPauseReason("review_unknown") {
		t.Fatalf("fallback: %+v %v", before, err)
	}
	reason := "review result: job_id and attempt_id must be 32 lowercase hex characters"
	if err := open.st.RecordReviewRejection(ctx, state.ReviewRejection{RunID: d.run, AttemptID: d.attempt, JobID: d.job, EffectID: d.effect, PayloadHash: hash, Reason: reason}); err != nil {
		t.Fatal(err)
	}
	got, err := durableStatus(open)
	if err != nil || !strings.Contains(got.PauseReason, reason) {
		t.Fatalf("pause: %+v %v", got, err)
	}
	if len(got.Tasks) != 1 || len(got.Tasks[0].Attempts) != 1 || got.Tasks[0].Attempts[0].ReviewRejection != reason {
		t.Fatalf("attempt reason: %+v", got.Tasks)
	}
	rc, out, stderr := runCapture(t, "build", "status", "--root", d.root, "--state-home", d.home, "--json")
	if rc != exitcode.OK || !strings.Contains(out, `"review_rejection"`) || !strings.Contains(out, reason) {
		t.Fatalf("JSON status: %d %s %s", rc, out, stderr)
	}
	rc, out, stderr = runCapture(t, "build", "status", "--root", d.root, "--state-home", d.home)
	if rc != exitcode.OK || !strings.Contains(out, reason) {
		t.Fatalf("human status: %d %s %s", rc, out, stderr)
	}
	if err := open.st.SetRunPausedFor(ctx, d.run, "review_unknown", strings.Repeat("9", 32), ""); err != nil {
		t.Fatal(err)
	}
	got, err = durableStatus(open)
	if err != nil || got.PauseReason != fixedPauseReason("review_unknown") {
		t.Fatalf("unrelated pause job: %+v %v", got, err)
	}
	if err := open.st.SetRunPausedFor(ctx, d.run, "review_unknown", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = durableStatus(open)
	if err != nil || !strings.Contains(got.PauseReason, reason) {
		t.Fatalf("current attempt fallback: %+v %v", got, err)
	}
	if err := open.st.ClearReviewRejection(ctx, d.job); err != nil {
		t.Fatal(err)
	}
	got, err = durableStatus(open)
	if err != nil || got.Tasks[0].Attempts[0].ReviewRejection != "" || got.PauseReason != fixedPauseReason("review_unknown") {
		t.Fatalf("cleared: %+v %v", got, err)
	}
}
