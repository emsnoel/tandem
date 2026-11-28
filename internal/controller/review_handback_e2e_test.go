package controller

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func handbackTestBin(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "tandem"), "./cmd/tandem")
	build.Dir = repo
	// Share the caller's Go cache rather than creating a cold module cache.
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real event CLI: %v\n%s", err, out)
	}
	if err := os.Symlink(filepath.Join(repo, "bin", "tandem-handback"), filepath.Join(bin, "tandem-handback")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// Exercise the exact command reviewers receive, including the installed shell
// helper and real event CLI. Only the external Herdr process is replaced.
func TestGeneratedReviewHandbackEndToEnd(t *testing.T) {
	t.Parallel()
	bin := handbackTestBin(t)
	for _, started := range []bool{false, true} {
		name := "verdict only"
		if started {
			name = "acknowledge then verdict"
		}
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			e.startReviewHandback()
			att := e.toReview("task-1", 1)
			prompt := e.effects(effectReviewPrompt)[0]
			dir := filepath.Join(RunDir(e.relay, e.runID), "review", prompt.ID)
			handback := filepath.Join(dir, "handback")
			queue := filepath.Join(RunDir(e.relay, e.runID), "q")
			queued := func() []string {
				t.Helper()
				files, err := filepath.Glob(filepath.Join(queue, "*.json"))
				if err != nil {
					t.Fatal(err)
				}
				return files
			}
			run := func(args ...string) ([]byte, error) {
				cmd := exec.Command(handback, args...)
				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
				var stderr strings.Builder
				cmd.Stderr = &stderr
				err := cmd.Run()
				return []byte(stderr.String()), err
			}
			if started {
				if out, err := run("--started"); err != nil {
					t.Fatalf("acknowledge: %v\n%s", err, out)
				}
				if len(queued()) != 1 {
					t.Fatalf("acknowledgement queued %d messages", len(queued()))
				}
				e.tick()
			}
			// A malformed verdict must fail locally, leaving no event for the
			// controller to reject. JSON is refused with the format to use.
			verdict := filepath.Join(dir, "verdict.txt")
			for body, want := range map[string]string{
				`{"schema_version":1,"status":"failed","findings":[{"message":"it requires: "Lint must pass.""}]}`: "plain text, not JSON",
				"status: changes_requested\n": "must say what to change",
			} {
				if err := os.WriteFile(verdict, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				out, err := run()
				if err == nil || !strings.Contains(string(out), want) {
					t.Fatalf("invalid verdict should fail with %q: %v\n%s", want, err, out)
				}
				if len(queued()) != 0 {
					t.Fatal("invalid verdict queued an event")
				}
			}
			// A finding that quotes the plan is recorded as written (#60).
			quoted := "Task 1, Step 5 explicitly requires: \"Lint must pass with no icon warnings.\" See `C:\\icons`."
			if err := os.WriteFile(verdict, []byte("status: changes_requested\n\nfinding: medium open greet.go:1-2\n"+quoted+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := run(); err != nil {
				t.Fatalf("submit verdict: %v\n%s", err, out)
			}
			files := queued()
			if len(files) != 1 {
				t.Fatalf("verdict queued %d events", len(files))
			}
			var envelope struct {
				Kind    string `json:"kind"`
				Attempt string `json:"attempt_id"`
				Effect  string `json:"effect_id"`
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Kind != EventReviewResult || envelope.Attempt != att.ID || envelope.Effect != prompt.ID {
				t.Fatalf("wrapper queued wrong binding: %s", raw)
			}
			e.tick()
			if e.reviewState(att) != workflow.ReviewPending && e.reviewState(att) != workflow.ReviewRunning {
				t.Fatalf("applied verdict before reviewer turn ended: %s", e.reviewState(att))
			}
			e.reviewerFinished(att, "reviewer-session")
			e.tick()
			if got := e.reviewState(att); got != workflow.ReviewChangesRequested {
				t.Fatalf("queued verdict did not apply: %s", got)
			}
			e.tick()
			if got := e.eventCount("review_completed"); got != 1 {
				t.Fatalf("review applied %d times", got)
			}
			hash, found, err := e.st.RecordedReviewResult(ctx, prompt.JobID)
			if err != nil || !found {
				t.Fatalf("no recorded result: %v", err)
			}
			raw, err = e.art.ReadAll(ctx, hash, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			var recorded workflow.ReviewResult
			if err := json.Unmarshal(raw, &recorded); err != nil || len(recorded.Findings) != 1 || recorded.Findings[0].Message != quoted ||
				recorded.Findings[0].Path != "greet.go" || *recorded.Findings[0].StartLine != 1 || *recorded.Findings[0].EndLine != 2 {
				t.Fatalf("recorded %s (%v); want the message %q as written", raw, err, quoted)
			}
		})
	}
}
