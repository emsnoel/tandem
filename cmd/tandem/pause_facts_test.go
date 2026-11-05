package main

import (
	"context"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// storedArtifact writes bytes as an artifact the store knows.
func storedArtifact(t *testing.T, open *openRun, body string) string {
	t.Helper()
	h, err := open.art.Put(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := open.st.AdoptArtifact(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return h
}

func openPaused(t *testing.T, code string) (*durable, *openRun) {
	t.Helper()
	d := pausedRun(t, code)
	open, err := openDurable(d.root, d.home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(open.close)
	return d, open
}

func reasonOf(t *testing.T, open *openRun) string {
	t.Helper()
	got, err := durableStatus(open)
	if err != nil {
		t.Fatal(err)
	}
	return got.PauseReason
}

func TestCheckFailedReasonIsTheEndOfTheCheckLog(t *testing.T) {
	d, open := openPaused(t, "check_failed")
	ctx := context.Background()
	log := strings.Repeat("noise line\n", 2000) + "FAIL TestReceipts: want 3 got 2\nexit status 1\n"
	h := storedArtifact(t, open, log)
	cfg := storedArtifact(t, open, "check config")
	if err := open.st.InsertJob(ctx, workflow.Job{ID: d.job, AttemptID: d.attempt, Kind: "check", State: "running", InputHash: h, ConfigHash: cfg, RunnerKind: "local-check"}); err != nil {
		t.Fatal(err)
	}
	if err := open.st.RecordCheckCertificate(ctx, state.CheckCertificate{ID: strings.Repeat("1", 32), AttemptID: d.attempt, JobID: d.job,
		BeforeInputsHash: h, AfterInputsHash: h, CommandHash: h, EnvironmentHash: h, LogHash: h, ExitCode: 1, Complete: true}); err != nil {
		t.Fatal(err)
	}
	got := reasonOf(t, open)
	if !strings.Contains(got, "FAIL TestReceipts: want 3 got 2") || !strings.HasSuffix(strings.TrimSpace(got), "exit status 1") {
		t.Fatalf("reason = %q", got)
	}
	if len(got) > 3000 {
		t.Fatalf("the reason is not bounded: %d bytes", len(got))
	}
}

func recordReview(t *testing.T, open *openRun, d *durable, status string, findings string) {
	t.Helper()
	h := storedArtifact(t, open, `{"status":"`+status+`","findings":[`+findings+`]}`)
	cfg := storedArtifact(t, open, "review config")
	if err := open.st.InsertJob(context.Background(), workflow.Job{ID: d.job, AttemptID: d.attempt, Kind: "reviewer", State: "running", InputHash: h, ConfigHash: cfg, PatchHash: h, TreeHash: h, RunnerKind: "pane-review"}); err != nil {
		t.Fatal(err)
	}
	if err := open.st.RecordReviewResult(context.Background(), state.ReviewRecord{JobID: d.job, AttemptID: d.attempt, ResultHash: h, PatchHash: h, TreeHash: h, Status: status, DistinctFromWriter: true}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewFailedAndRepairsExhaustedReasonsAreTheFindings(t *testing.T) {
	for _, code := range []string{"review_failed", "repairs_exhausted"} {
		t.Run(code, func(t *testing.T) {
			d, open := openPaused(t, code)
			recordReview(t, open, d, "failed", `{"severity":"high","path":"store.go","disposition":"open","message":"the receipt is written twice","start_line":null,"end_line":null}`)
			got := reasonOf(t, open)
			if !strings.Contains(got, "failed") || !strings.Contains(got, "store.go") || !strings.Contains(got, "the receipt is written twice") {
				t.Fatalf("reason = %q", got)
			}
		})
	}
}

func TestReviewFindingsAreBounded(t *testing.T) {
	d, open := openPaused(t, "review_failed")
	var fs []string
	for i := 0; i < 40; i++ {
		fs = append(fs, `{"severity":"low","path":"a.go","disposition":"open","message":"`+strings.Repeat("x", 300)+`","start_line":null,"end_line":null}`)
	}
	recordReview(t, open, d, "failed", strings.Join(fs, ","))
	if got := reasonOf(t, open); len(got) == 0 || len(got) > 2500 {
		t.Fatalf("reason is %d bytes", len(got))
	}
}

func TestRecoveryBlockedReasonIsTheIncidentWhy(t *testing.T) {
	d, open := openPaused(t, "recovery_blocked")
	pol := storedArtifact(t, open, "policy")
	if err := open.st.CreateCollaboration(context.Background(), state.CollaborationRequest{ID: strings.Repeat("7", 32), PolicyHash: pol}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := open.st.OpenIncident(context.Background(), state.IncidentSpec{Key: strings.Repeat("2", 32), CollaborationID: strings.Repeat("7", 32), RunID: d.run, TaskID: "receipt-store", AttemptID: d.attempt,
		Kind: "check_failed", Reason: "the failure names no concrete target to fix"}); err != nil {
		t.Fatal(err)
	}
	if got := reasonOf(t, open); !strings.Contains(got, "the failure names no concrete target to fix") {
		t.Fatalf("reason = %q", got)
	}
}

func TestVerificationReasonsNameTheAttempt(t *testing.T) {
	for code, want := range map[string]string{"verification_identity": "attempt 1 of receipt-store", "verification_stale": "attempt 1 of receipt-store"} {
		_, open := openPaused(t, code)
		if got := reasonOf(t, open); !strings.Contains(got, want) {
			t.Errorf("%s: reason = %q, want %q", code, got, want)
		}
	}
}

func TestCodesWithoutStoredFactsHaveNoReason(t *testing.T) {
	for _, code := range []string{"no_reviewer", "context_incomplete", "memory_conflict", "stopped"} {
		_, open := openPaused(t, code)
		if got := reasonOf(t, open); got != "" {
			t.Errorf("%s: reason = %q, want none", code, got)
		}
	}
}
