package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/decision"
)

// With no credential available the feature is simply off: exit 0, no output,
// no network. Absence is a normal state, not an error.
func TestDecisionAssessIsSilentWithoutACredential(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("# Plan\nChange one constant.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("HOME", dir)

	if rc := cmdDecision([]string{"assess", "--plan", plan, "--root", dir, "--render"}); rc != 0 {
		t.Fatalf("want exit 0 when the feature is off, got %d", rc)
	}
}

// --dry-run must never reach the network, and must show exactly what would be
// sent so a human can check it before turning the feature on.
func TestDecisionDryRunPrintsThePayloadAndSendsNothing(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("# Plan\nexport API_KEY=supersecret123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "not-a-real-key")

	out := captureStdout(t, func() int {
		return cmdDecision([]string{"assess", "--plan", plan, "--root", dir, "--dry-run"})
	})
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("a dry run must show the redacted payload, got %q", out)
	}
	if strings.Contains(out, "supersecret123") {
		t.Fatalf("the dry run leaked a secret: %q", out)
	}
}

func TestDecisionRejectsAMissingPlan(t *testing.T) {
	if rc := cmdDecision([]string{"assess", "--plan", "/nonexistent/plan.md", "--root", "/tmp"}); rc == 0 {
		t.Fatal("want a non-zero exit for an unreadable plan")
	}
}

// staffingBatch is a well-formed staffing answer set for a mechanical,
// parallel plan; conf is how sure Jev is of that shape.
func staffingBatch(conf float64) map[string]decision.Answer {
	return map[string]decision.Answer{
		decision.QShape:       {Type: "choice", Choice: string(decision.ShapeMechanical), Confidence: conf},
		decision.QRoutine:     {Type: "score", Score: 3},
		decision.QSecurity:    {Type: "noul", Noul: 0},
		decision.QDestructive: {Type: "noul", Noul: 0},
		decision.QExternal:    {Type: "noul", Noul: 0},
		decision.QParallel:    {Type: "noul", Noul: 0.9},
	}
}

// assessWithStatus runs `decision assess --render --status F` and returns
// what reached stdout (the prompt) and what reached F (for the human).
func assessWithStatus(t *testing.T, key string, f *fakeJudge) (stdout, status string) {
	t.Helper()
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("# Plan\nChange one constant.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", key)
	if f != nil {
		useFakeJudge(t, f)
	}
	st := filepath.Join(dir, "status")
	stdout = captureStdout(t, func() int {
		return cmdDecision([]string{"assess", "--plan", plan, "--root", dir, "--render", "--status", st})
	})
	b, _ := os.ReadFile(st)
	return stdout, string(b)
}

// The prompt is silent on every non-advice path, so only the status file
// tells "Jev said nothing" from "never asked".
func TestDecisionStatusSaysOffWithoutAKey(t *testing.T) {
	out, st := assessWithStatus(t, "", nil)
	if out != "" || strings.TrimSpace(st) != "off" {
		t.Fatalf("stdout %q, status %q; want nothing and off", out, st)
	}
}

func TestDecisionStatusSaysAdviceWhenJevAdvises(t *testing.T) {
	f := &fakeJudge{answers: staffingBatch(0.95)}
	out, st := assessWithStatus(t, "k", f)
	if !strings.Contains(out, "<staffing-advice") || strings.TrimSpace(st) != "advice" {
		t.Fatalf("stdout %q, status %q; want the advisory and advice", out, st)
	}
}

// Jev answered but was not sure of the plan's shape: nothing reaches the
// prompt, and the status says Jev WAS asked.
func TestDecisionStatusSaysNoAdviceWhenJevIsUnsure(t *testing.T) {
	f := &fakeJudge{answers: staffingBatch(0.5)}
	out, st := assessWithStatus(t, "k", f)
	if out != "" || strings.TrimSpace(st) != "no_advice" || f.calls != 1 {
		t.Fatalf("stdout %q, status %q, calls %d; want nothing, no_advice, 1 call", out, st, f.calls)
	}
}

func TestDecisionStatusCarriesTheErrorReason(t *testing.T) {
	f := &fakeJudge{err: errors.New("jev: request timed out")}
	out, st := assessWithStatus(t, "k", f)
	lines := strings.SplitN(st, "\n", 2)
	if out != "" || lines[0] != "error" || !strings.Contains(st, "timed out") {
		t.Fatalf("stdout %q, status %q; want nothing and error with the reason", out, st)
	}
}
