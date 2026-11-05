package main

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/exitcode"
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

// artifactFile finds the stored file of an artifact under the state home.
func artifactFile(t *testing.T, d *durable, hash string) string {
	t.Helper()
	var found string
	filepath.WalkDir(d.home, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == hash {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no file for artifact %s under %s", hash, d.home)
	}
	return found
}

// seedFailedCheck records a failed check whose log is body, returning the log's hash.
func seedFailedCheck(t *testing.T, open *openRun, d *durable, body string) string {
	t.Helper()
	ctx := context.Background()
	h := storedArtifact(t, open, body)
	cfg := storedArtifact(t, open, "check config")
	if err := open.st.InsertJob(ctx, workflow.Job{ID: d.job, AttemptID: d.attempt, Kind: "check", State: "running", InputHash: h, ConfigHash: cfg, RunnerKind: "local-check"}); err != nil {
		t.Fatal(err)
	}
	if err := open.st.RecordCheckCertificate(ctx, state.CheckCertificate{ID: strings.Repeat("1", 32), AttemptID: d.attempt, JobID: d.job,
		BeforeInputsHash: h, AfterInputsHash: h, CommandHash: h, EnvironmentHash: h, LogHash: h, ExitCode: 1, Complete: true}); err != nil {
		t.Fatal(err)
	}
	return h
}

// A fact that cannot be read is dropped. Status, wait and the note still work.
func TestUnreadableFactsNeverFailStatus(t *testing.T) {
	cases := map[string]func(t *testing.T, d *durable, open *openRun){
		"check log missing": func(t *testing.T, d *durable, open *openRun) {
			os.Remove(artifactFile(t, d, seedFailedCheck(t, open, d, "gone soon\n")))
		},
		"check log corrupt": func(t *testing.T, d *durable, open *openRun) {
			f := artifactFile(t, d, seedFailedCheck(t, open, d, "will be damaged\n"))
			os.Chmod(f, 0o644)
			if err := os.WriteFile(f, []byte("damaged"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			d, open := openPaused(t, "check_failed")
			setup(t, d, open)
			got, err := durableStatus(open)
			if err != nil || got.PauseReason != "" || got.State != "paused" {
				t.Fatalf("status = %+v, %v", got, err)
			}
			for _, args := range [][]string{{"status", "--fields"}, {"wait", "--until", "paused", "--timeout", "0", "--fields"}} {
				rc, out, errOut := runCapture(t, append(append([]string{"build"}, args...), "--root", d.root, "--state-home", d.home)...)
				if rc != exitcode.OK || !strings.HasPrefix(out, "paused\x00check_failed\x00") {
					t.Fatalf("build %v: rc %d out %q err %q", args, rc, out, errOut)
				}
			}
		})
	}
}

func TestUnparsableReviewResultIsDropped(t *testing.T) {
	for _, code := range []string{"review_failed", "repairs_exhausted"} {
		d, open := openPaused(t, code)
		_ = d
		h := storedArtifact(t, open, "this is not json")
		cfg := storedArtifact(t, open, "review config")
		if err := open.st.InsertJob(context.Background(), workflow.Job{ID: d.job, AttemptID: d.attempt, Kind: "reviewer", State: "running", InputHash: h, ConfigHash: cfg, PatchHash: h, TreeHash: h, RunnerKind: "pane-review"}); err != nil {
			t.Fatal(err)
		}
		if err := open.st.RecordReviewResult(context.Background(), state.ReviewRecord{JobID: d.job, AttemptID: d.attempt, ResultHash: h, PatchHash: h, TreeHash: h, Status: "failed", DistinctFromWriter: true}); err != nil {
			t.Fatal(err)
		}
		got, err := durableStatus(open)
		if err != nil || got.PauseReason != "" {
			t.Fatalf("%s: status = %+v, %v", code, got, err)
		}
	}
}

func TestMissingReviewResultIsDropped(t *testing.T) {
	d, open := openPaused(t, "review_failed")
	recordReview(t, open, d, "failed", `{"severity":"high","path":"a.go","disposition":"open","message":"m","start_line":null,"end_line":null}`)
	jobs, _ := open.st.AttemptJobs(context.Background(), d.attempt, "reviewer")
	os.Remove(artifactFile(t, d, jobs[0].ResultHash))
	if got, err := durableStatus(open); err != nil || got.PauseReason != "" {
		t.Fatalf("status = %+v, %v", got, err)
	}
}

func TestUnreadableDispatchReasonIsDropped(t *testing.T) {
	for _, body := range []string{"not json", ""} {
		d, open := openPaused(t, "dispatch_failed")
		h := storedArtifact(t, open, "not json")
		db := rawDB(t, d)
		defer db.Close()
		if _, err := db.Exec(`UPDATE effects SET state='failed', not_sent_hash=? WHERE id=?`, h, d.effect); err != nil {
			t.Fatal(err)
		}
		if body == "" {
			os.Remove(artifactFile(t, d, h))
		}
		if got, err := durableStatus(open); err != nil || got.PauseReason != "" {
			t.Fatalf("%q: status = %+v, %v", body, got, err)
		}
	}
}

func rawDB(t *testing.T, d *durable) *sql.DB {
	t.Helper()
	id, err := state.ResolveIdentity(d.root, d.home)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(id.StateRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFactsAreCleanAndBounded(t *testing.T) {
	d, open := openPaused(t, "check_failed")
	// One huge last line with colors, a bell and multi-byte text at the cut.
	line := "\x1b[31mFAIL\x1b[0m\a " + strings.Repeat("é", 4000) + " the real error"
	seedFailedCheck(t, open, d, "first\n"+line)
	got := reasonOf(t, open)
	if !strings.Contains(got, "the real error") {
		t.Fatalf("a long last line lost its end: %q", got)
	}
	for _, r := range got {
		if r == 0x1b || r == 7 || r == utf8.RuneError {
			t.Fatalf("control or broken byte %q in %q", r, got)
		}
	}
	if !utf8.ValidString(got) || len(got) > 3000 {
		t.Fatalf("not valid or not bounded: %d", len(got))
	}
}

func TestLongIncidentReasonAndPathAreBounded(t *testing.T) {
	d, open := openPaused(t, "recovery_blocked")
	pol := storedArtifact(t, open, "policy")
	open.st.CreateCollaboration(context.Background(), state.CollaborationRequest{ID: strings.Repeat("7", 32), PolicyHash: pol})
	open.st.OpenIncident(context.Background(), state.IncidentSpec{Key: strings.Repeat("2", 32), CollaborationID: strings.Repeat("7", 32), RunID: d.run, TaskID: "receipt-store", AttemptID: d.attempt,
		Kind: "check_failed", Reason: strings.Repeat("é", 5000) + "\x1b[31m"})
	got := reasonOf(t, open)
	if got == "" || len(got) > 700 || !utf8.ValidString(got) || strings.ContainsRune(got, 0x1b) {
		t.Fatalf("incident reason: %d bytes, %q", len(got), got)
	}
}

func TestLongFindingPathIsBounded(t *testing.T) {
	d, open := openPaused(t, "review_failed")
	recordReview(t, open, d, "failed", `{"severity":"high","path":"`+strings.Repeat("p/", 2000)+`","disposition":"open","message":"m","start_line":null,"end_line":null}`)
	if got := reasonOf(t, open); len(got) > 1000 {
		t.Fatalf("path not bounded: %d", len(got))
	}
}
