package state

import (
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// verificationEnv is a seeded attempt with evidence and an independent
// verification reviewer job.
func verificationEnv(t *testing.T) (*Store, *seed, string) {
	t.Helper()
	s, sd, job, _, _ := reviewJobEnv(t)
	if err := s.RegisterReviewStage(ctx, job, StageIndependentVerification); err != nil {
		t.Fatal(err)
	}
	sd.exec(t, `UPDATE attempts SET end_snapshot_hash=?, patch_hash=? WHERE id=?`, sd.StartHash, sd.PacketHash, sd.AttemptID)
	return s, sd, job
}

func record(t *testing.T, s *Store, sd *seed, job, status string, distinct bool, patch, tree string) {
	t.Helper()
	if err := s.RecordReviewResult(ctx, ReviewRecord{JobID: job, AttemptID: sd.AttemptID, ResultHash: sd.artifact(t, "result "+status),
		PatchHash: patch, TreeHash: tree, Status: status, DistinctFromWriter: distinct}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceVerificationOutcomeNeedsDistinctCurrentEvidence(t *testing.T) {
	type want struct{ found, distinct, valid bool }
	for name, tc := range map[string]struct {
		status   string
		distinct bool
		stale    bool
		want     want
	}{
		"a distinct pass":                  {"passed", true, false, want{true, true, true}},
		"a pass by the writer":             {"passed", false, false, want{true, false, false}},
		"a distinct pass for another tree": {"passed", true, true, want{true, true, false}},
		"a distinct request for changes":   {"changes_requested", true, false, want{true, true, true}},
	} {
		s, sd, job := verificationEnv(t)
		if got, _ := s.AttemptVerification(ctx, sd.AttemptID); got.Found {
			t.Fatalf("%s: a verification before any result: %+v", name, got)
		}
		tree := sd.StartHash
		if tc.stale {
			tree = sd.PacketHash
		}
		record(t, s, sd, job, tc.status, tc.distinct, sd.PacketHash, tree)
		got, err := s.AttemptVerification(ctx, sd.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Found != tc.want.found || got.Distinct != tc.want.distinct || got.Valid != tc.want.valid ||
			got.Status != workflow.ReviewState(tc.status) || got.JobID != job {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// The builder review reads only builder-stage results: a verification result,
// distinct or not, is another stage's evidence.
func TestAcceptanceVerificationIsNotTheBuilderReview(t *testing.T) {
	s, sd, job := verificationEnv(t)
	record(t, s, sd, job, "passed", true, sd.PacketHash, sd.StartHash)
	att, err := s.GetAttempt(ctx, sd.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AcceptanceFacts(ctx, att, workflow.Observed{RequireVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.ReviewResultValid || f.ReviewStatus != "" {
		t.Fatalf("a verification read as the builder review: valid %v, status %q", f.ReviewResultValid, f.ReviewStatus)
	}
	if !f.VerificationValid || f.VerificationStatus != workflow.ReviewPassed {
		t.Fatalf("the verification itself was not read: %+v", f)
	}
	if h, ok, _ := s.LatestReviewResult(ctx, sd.AttemptID); !ok || h == "" {
		t.Fatal("the latest result, used for a repair's findings, must include the verification")
	}
}
