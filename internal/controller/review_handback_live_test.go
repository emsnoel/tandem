package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// This opt-in provider smoke test verifies that a real sandboxed Codex reviewer
// can execute the generated command without copying any protocol identifiers.
func TestLiveCodexReviewHandback(t *testing.T) {
	if os.Getenv("TANDEM_LIVE_REVIEW_HANDBACK") != "1" {
		t.Skip("set TANDEM_LIVE_REVIEW_HANDBACK=1 to run the live Codex workspace-write smoke test")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "tandem"), "./cmd/tandem")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	if err := os.Symlink(filepath.Join(repo, "bin", "tandem-handback"), filepath.Join(bin, "tandem-handback")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	promptEffect := e.effects(effectReviewPrompt)[0]
	dir := filepath.Join(RunDir(e.relay, e.runID), "review", promptEffect.ID)
	prompt := fmt.Sprintf("This is an authorized isolated protocol smoke test, not a code review. Write exactly {\"schema_version\":1,\"status\":\"passed\",\"findings\":[]} to %q. Then execute %q with no arguments and stop. Do not copy or type any run, attempt, job, effect, or nonce identifiers. The executable command already binds them. Use the inherited PATH for tandem-handback and tandem. Do not change any other file or run other tools. Report the command result.", filepath.Join(dir, "verdict.json"), filepath.Join(dir, "handback"))
	// Preserve the provider transcript after Go removes its fixture directories.
	log, err := os.CreateTemp("", "tandem-review-handback-live-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	t.Logf("live Codex transcript: %s", log.Name())
	bounded, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, codex, "exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "--cd", e.root, prompt)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		t.Fatalf("live Codex failed: %v (deadline: %v); transcript %s", err, bounded.Err(), log.Name())
	}
	queue, err := filepath.Glob(filepath.Join(RunDir(e.relay, e.runID), "q", "*.json"))
	if err != nil || len(queue) != 1 {
		t.Fatalf("expected one real queued verdict, got %d: %v; transcript %s", len(queue), err, log.Name())
	}
	raw, err := os.ReadFile(queue[0])
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Attempt       string `json:"attempt_id"`
		Effect        string `json:"effect_id"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != 3 || event.Kind != EventReviewResult || event.Attempt != att.ID || event.Effect != promptEffect.ID {
		t.Fatalf("unexpected live envelope: %s", raw)
	}
	e.tick()
	if e.reviewState(att) == workflow.ReviewPassed {
		t.Fatal("live verdict applied before reviewer settled")
	}
	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	if got := e.reviewState(att); got != workflow.ReviewPassed {
		t.Fatalf("live verdict not accepted: %s; transcript %s", got, log.Name())
	}
	e.tick()
	if got := e.eventCount("review_completed"); got != 1 {
		t.Fatalf("live verdict applied %d times", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "verdict.json")); err != nil || strings.Contains(string(b), "attempt_id") {
		t.Fatalf("reviewer did not leave a verdict-only file: %s: %v", b, err)
	}
	t.Log("real workspace-write Codex queued a v3 verdict; controller applied it once after reviewer settlement")
}
