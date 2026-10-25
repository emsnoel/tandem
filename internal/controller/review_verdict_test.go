package controller

import (
	"errors"
	"github.com/emsnoel/tandem/internal/runner"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func TestVerdictOnlyMailboxBindsTheStoredJob(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	prompt := e.effects(effectReviewPrompt)[0]
	e.sendReview(att, prompt, EventReviewResult, `{"schema_version":1,"status":"passed","findings":[]}`)
	e.tick()
	if e.reviewState(att) == workflow.ReviewPassed {
		t.Fatal("accepted before reviewer settled")
	}
	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	if e.reviewState(att) != workflow.ReviewPassed {
		t.Fatalf("verdict not bound: %s", e.reviewState(att))
	}
	e.tick()
	if e.eventCount("review_completed") != 1 {
		t.Fatal("verdict replay applied twice")
	}
}

func TestVerdictOnlyRefusesChangedTree(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	e.sendReview(att, e.effects(effectReviewPrompt)[0], EventReviewResult, `{"schema_version":1,"status":"passed","findings":[]}`)
	e.reviewerFinished(att, "reviewer-session")
	e.work(99)
	e.tick()
	if st, code := e.runState(); st != "paused" || code != pauseSourceMutated {
		t.Fatalf("run %s/%s", st, code)
	}
	if e.eventCount("review_completed") != 0 {
		t.Fatal("bound changed tree")
	}
}

func TestGeneratedReviewHandbackBindsArgumentsAndPreservesVerdict(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo with ' quotes")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{Root: root, ReviewerSession: "reviewer", PlannerSession: "planner"}
	p := fixturePacket(stageReview)
	if err := prepareReviewHandback(cfg, &p); err != nil {
		t.Fatal(err)
	}
	verdict := filepath.Join(p.HandbackDir, "verdict.json")
	want := `{"schema_version":1,"status":"passed","findings":[]}`
	if err := os.WriteFile(verdict, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareReviewHandback(cfg, &p); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(verdict); string(got) != want {
		t.Fatal("retry erased verdict")
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tandem-handback"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--started"}} {
		cmd := exec.Command(filepath.Join(p.HandbackDir, "handback"), args...)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wrapper: %v %s", err, out)
		}
		for _, v := range []string{root, p.RunID, p.AttemptID, p.JobID, p.EffectID, p.Nonce, "--role\nreviewer"} {
			if !strings.Contains(string(out), v) {
				t.Errorf("missing %q in %s", v, out)
			}
		}
		if len(args) == 0 && !strings.Contains(string(out), verdict) {
			t.Fatal("no bound verdict path")
		}
		if len(args) > 0 && !strings.Contains(string(out), "--review-started") {
			t.Fatal("no prebound acknowledgement")
		}
	}
}

func TestMalformedPaneReviewReportsReasonAndAcceptsCorrection(t *testing.T) {
	e := newCEnv(t)
	att := e.toDeliveredReview()
	e.reviewerFinished(att, "reviewer-session")
	result := e.resultFor(att, workflow.ReviewStatusPassed)
	result.AttemptID += "d"
	e.tr.setPane("reviewer-session", string(encode(result)))
	e.tick()
	rejects, err := e.st.RunReviewRejections(ctx, e.runID)
	if err != nil || len(rejects) != 1 || !strings.Contains(rejects[0].Reason, "job_id and attempt_id must be 32 lowercase hex characters") {
		t.Fatalf("rejections %+v: %v", rejects, err)
	}
	before := e.tr.attempts
	e.tick()
	if e.tr.attempts != before {
		t.Fatal("same rejection notified again")
	}
	result.AttemptID = att.ID
	e.tr.setPane("reviewer-session", string(encode(result)))
	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	if e.reviewState(att) != workflow.ReviewPassed {
		t.Fatal("corrected reply refused")
	}
	rejects, _ = e.st.RunReviewRejections(ctx, e.runID)
	if len(rejects) != 0 {
		t.Fatal("successful review retained current rejection")
	}
}

func TestReviewRejectionNotificationRespectsEnforcedBudget(t *testing.T) {
	e := newCEnv(t)
	e.reviewerKind = "codex"
	e.startEnforced(10000)
	att := e.toReview("task-1", 1)
	e.reviewerFinished(att, "reviewer-session")
	bad := e.resultFor(att, workflow.ReviewStatusPassed)
	bad.AttemptID += "d"
	e.tr.setPane("reviewer-session", string(encode(bad)))
	e.tick()
	rejects, err := e.st.RunReviewRejections(ctx, e.runID)
	if err != nil || len(rejects) != 1 || !rejects[0].Notified {
		t.Fatalf("feedback not admitted: %+v %v", rejects, err)
	}
	before := e.tr.attempts
	e.tick()
	if e.tr.attempts != before {
		t.Fatal("budgeted rejection sent twice")
	}
}

func TestReviewRejectionClaimsNotificationBeforeSending(t *testing.T) {
	e := newCEnv(t)
	att := e.toDeliveredReview()
	e.reviewerFinished(att, "reviewer-session")
	bad := e.resultFor(att, workflow.ReviewStatusPassed)
	bad.AttemptID += "d"
	e.tr.setPane("reviewer-session", string(encode(bad)))
	claimed := false
	e.tr.sendErr = func(d runner.Delivery) error {
		rejects, err := e.st.RunReviewRejections(ctx, e.runID)
		claimed = err == nil && len(rejects) == 1 && rejects[0].Notified
		return errors.New("socket closed after possibly sending")
	}
	e.tick()
	if !claimed {
		t.Fatal("feedback was not durably claimed before sending")
	}
	e.tr.sendErr = nil
	before := e.tr.attempts
	e.tick()
	if e.tr.attempts != before {
		t.Fatal("uncertain send retried")
	}
}

func TestMalformedMailboxReviewCanBeCorrectedWithoutReservingCandidate(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	prompt := e.effects(effectReviewPrompt)[0]
	bad := e.resultFor(att, workflow.ReviewStatusPassed)
	bad.AttemptID += "d"
	if err := e.sendReviewError(att, prompt, EventReviewResult, string(encode(bad))); err == nil || !strings.Contains(err.Error(), "job_id and attempt_id must be 32 lowercase hex characters") {
		t.Fatalf("bad result: %v", err)
	}
	if _, found, _ := e.st.ReviewCandidateFor(ctx, prompt.JobID); found {
		t.Fatal("invalid submission reserved candidate")
	}
	rejectionFile := filepath.Join(RunDir(e.relay, e.runID), "review", prompt.ID, "rejected.txt")
	if raw, err := os.ReadFile(rejectionFile); err != nil || !strings.Contains(string(raw), "32 lowercase hex") {
		t.Fatalf("rejection file: %s %v", raw, err)
	}
	e.sendReview(att, prompt, EventReviewResult, `{"schema_version":1,"status":"passed","findings":[]}`)
	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	if e.reviewState(att) != workflow.ReviewPassed {
		t.Fatal("corrected submission was not applied")
	}
}

// Stop tells the reviewer not to hand anything back, so a verdict that still
// arrives is staged as before and never answered with correction feedback.
func TestReviewHandbackAfterStopQueuesNoFeedback(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	prompt := e.effects(effectReviewPrompt)[0]
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if err := e.sendReviewError(att, prompt, EventReviewResult, `{"schema_version":1,"status":"passed","findings":[]}`); err != nil {
		t.Fatalf("handback after stop: %v", err)
	}
	rejects, err := e.st.RunReviewRejections(ctx, e.runID)
	if err != nil || len(rejects) != 0 {
		t.Fatalf("feedback queued for a stopped run: %+v %v", rejects, err)
	}
}
