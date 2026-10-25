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

// Exercise the exact command reviewers receive, including the installed shell
// helper and real event CLI. Only the external Herdr process is replaced.
func TestGeneratedReviewHandbackEndToEnd(t *testing.T) {
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
			// controller to reject.
			verdict := filepath.Join(dir, "verdict.json")
			if err := os.WriteFile(verdict, []byte(`{"schema_version":1,"status":"changes_requested","findings":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := run()
			if err == nil || !strings.Contains(string(out), "must say what to change") {
				t.Fatalf("invalid verdict should fail with actionable error: %v\n%s", err, out)
			}
			if len(queued()) != 0 {
				t.Fatal("invalid verdict queued an event")
			}
			if err := os.WriteFile(verdict, []byte(`{"schema_version":1,"status":"passed","findings":[]}`), 0600); err != nil {
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
			if e.reviewState(att) == workflow.ReviewPassed {
				t.Fatal("accepted verdict before reviewer turn ended")
			}
			e.reviewerFinished(att, "reviewer-session")
			e.tick()
			if got := e.reviewState(att); got != workflow.ReviewPassed {
				t.Fatalf("queued verdict did not apply: %s", got)
			}
			e.tick()
			if got := e.eventCount("review_completed"); got != 1 {
				t.Fatalf("review applied %d times", got)
			}
		})
	}
}
