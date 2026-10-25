package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// checkFixture is an attempt that has submitted and is waiting for its check.
func checkFixture(t *testing.T) (*Store, *seed) {
	t.Helper()
	s, sd, _ := newSeededStore(t)
	sd.submitReady(t)
	sd.setAttempt(t, `implementation_state='submitted', check_state='pending'`)
	return s, sd
}

func (sd *seed) newJob(t *testing.T, n int, kind string) workflow.Job {
	return workflow.Job{ID: hexID(7000 + n), AttemptID: sd.AttemptID, Kind: kind, State: "running",
		InputHash: sd.artifact(t, "job input"), ConfigHash: sd.artifact(t, "job config"), RunnerKind: "local"}
}

func (sd *seed) certificate(t *testing.T, job workflow.Job, mutate func(*CheckCertificate)) CheckCertificate {
	in := sd.artifact(t, "inputs before")
	c := CheckCertificate{ID: hexID(8000), AttemptID: sd.AttemptID, JobID: job.ID,
		BeforeInputsHash: in, AfterInputsHash: in, CommandHash: sd.artifact(t, "command"),
		EnvironmentHash: sd.artifact(t, "environment"), LogHash: sd.artifact(t, "the log"), ExitCode: 0, Complete: true}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func TestJobLifecycleAndCertificate(t *testing.T) {
	s, sd := checkFixture(t)
	job := sd.newJob(t, 1, "check")
	if err := s.InsertJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertJob(ctx, job); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a job id used twice: %v", err)
	}
	var state string
	s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state)
	if state != "running" {
		t.Fatalf("job state %q", state)
	}
	cert := sd.certificate(t, job, nil)
	if err := s.RecordCheckCertificate(ctx, cert); err != nil {
		t.Fatal(err)
	}
	var result string
	var done *int64
	s.db.QueryRow(`SELECT state,result_hash,completed_ms FROM jobs WHERE id=?`, job.ID).Scan(&state, &result, &done)
	if state != "completed" || result != cert.LogHash || done == nil || *done != 5000 {
		t.Fatalf("job %s result %s completed %v", state, result, done)
	}
	if sd.count(t, "check_certificates") != 1 {
		t.Fatal("certificate not stored")
	}
	// A job completes once.
	cert.ID = hexID(8001)
	if err := s.RecordCheckCertificate(ctx, cert); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("second certificate for one job: %v", err)
	}
}

func TestCertificateIsRefusedForTheWrongJob(t *testing.T) {
	s, sd := checkFixture(t)
	other := seedProjectRunTaskAttempt(t, s, 2)
	reviewer := sd.newJob(t, 2, "reviewer")
	s.InsertJob(ctx, reviewer)
	foreign := other.newJob(t, 3, "check")
	s.InsertJob(ctx, foreign)
	mine := sd.newJob(t, 4, "check")
	s.InsertJob(ctx, mine)

	for name, c := range map[string]CheckCertificate{
		"not a check job":       sd.certificate(t, reviewer, nil),
		"another attempt's job": sd.certificate(t, foreign, nil),
		"unknown job":           sd.certificate(t, workflow.Job{ID: hexID(9999)}, nil),
		"bad hash":              sd.certificate(t, mine, func(c *CheckCertificate) { c.LogHash = "nope" }),
		"unstored hash":         sd.certificate(t, mine, func(c *CheckCertificate) { c.LogHash = hexID(1) + hexID(2) }),
	} {
		if err := s.RecordCheckCertificate(ctx, c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if sd.count(t, "check_certificates") != 0 {
		t.Fatal("a refused certificate left a row")
	}
	var st string
	s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, mine.ID).Scan(&st)
	if st != "running" {
		t.Fatalf("a refused certificate completed the job: %s", st)
	}
}

func TestFinishJobRecordsAFailureWithoutACertificate(t *testing.T) {
	s, sd := checkFixture(t)
	job := sd.newJob(t, 1, "check")
	s.InsertJob(ctx, job)
	if err := s.FinishJob(ctx, job.ID, "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishJob(ctx, job.ID, "completed"); err == nil {
		t.Fatal("FinishJob may not mark a job completed; only a certificate does")
	}
	if err := s.RecordCheckCertificate(ctx, sd.certificate(t, job, nil)); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a cancelled job accepted a certificate: %v", err)
	}
}

// The builder never supplies a check outcome: check_completed reads the
// controller's certificate, and a certificate that does not bind one set of
// inputs proves nothing.
func TestCheckCompletedReadsOnlyThePersistedCertificate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*CheckCertificate)
		wantErr error
		check   string
	}{
		{"clean pass", nil, nil, "passed"},
		{"failing exit", func(c *CheckCertificate) { c.ExitCode = 1 }, nil, "failed"},
		{"inputs changed during the check", func(c *CheckCertificate) { c.AfterInputsHash = c.CommandHash }, workflow.ErrIncompleteEvidence, "pending"},
		{"incomplete capture", func(c *CheckCertificate) { c.Complete = false }, workflow.ErrIncompleteEvidence, "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sd := checkFixture(t)
			job := sd.newJob(t, 1, "check")
			s.InsertJob(ctx, job)
			if err := s.RecordCheckCertificate(ctx, sd.certificate(t, job, tc.mutate)); err != nil {
				t.Fatal(err)
			}
			v := sd.version(t)
			_, err := s.ApplyWith(ctx, sd.event(1, workflow.EventCheckCompleted, v), workflow.Observed{SchedulingAllowed: true, NoReviewPolicy: true})
			if !errors.Is(err, tc.wantErr) && !(tc.wantErr == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			var check string
			s.db.QueryRow(`SELECT check_state FROM attempts WHERE id=?`, sd.AttemptID).Scan(&check)
			if check != tc.check {
				t.Fatalf("check state %s, want %s", check, tc.check)
			}
		})
	}
}

func TestAdoptArtifactVerifiesBeforeItRecords(t *testing.T) {
	s, sd, root := newSeededStore(t)
	store := artifactReader(t, s)
	h, err := store.Put(ctx, strings.NewReader("adopt me"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptArtifact(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptArtifact(ctx, h); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
	var size int64
	s.db.QueryRow(`SELECT size_bytes FROM artifacts WHERE hash=?`, h).Scan(&size)
	if size != 8 {
		t.Fatalf("size %d", size)
	}
	if err := s.AdoptArtifact(ctx, strings.Repeat("a", 64)); err == nil {
		t.Fatal("an artifact that is not stored was adopted")
	}
	// A corrupted object is refused.
	h2, _ := store.Put(ctx, strings.NewReader("will be damaged"))
	path := filepath.Join(root, "artifacts", "sha256", h2[:2], h2)
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte("damaged!!"), 0o600)
	if err := s.AdoptArtifact(ctx, h2); !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("err = %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM artifacts WHERE hash=?`, h2).Scan(&n)
	if n != 0 || sd == nil {
		t.Fatal("a corrupt artifact left a row")
	}
}

func TestGetCheckCertificateRoundTrips(t *testing.T) {
	s, sd := checkFixture(t)
	job := sd.newJob(t, 1, "check")
	s.InsertJob(ctx, job)
	cert := sd.certificate(t, job, func(c *CheckCertificate) { c.ExitCode = 7 })
	if err := s.RecordCheckCertificate(ctx, cert); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetCheckCertificate(ctx, cert.ID)
	if err != nil || !ok || got != cert {
		t.Fatalf("got %+v ok %v err %v, want %+v", got, ok, err, cert)
	}
	if _, ok, _ := s.GetCheckCertificate(ctx, hexID(1)); ok {
		t.Fatal("found a certificate that does not exist")
	}
}
