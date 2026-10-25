package controller

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

// bigFirstTask is a plan whose first task's brief is about 6 KB.
var bigFirstTask = "# Greeting\n\n**Goal:** greet.\n\n### Task 1: First thing\n\n" +
	strings.Repeat("Do the first thing, carefully.\n", 200) + "\n### Task 2: Second thing\n\nDo the second thing.\n"

func (e *cenv) usePlan(text string) {
	e.t.Helper()
	if err := os.WriteFile(e.plan, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
	git(e.t, e.root, "add", ".")
	git(e.t, e.root, "commit", "-qm", "plan")
}

func (e *cenv) preflight(limit int) error {
	return CheckTaskContext(e.startRequest(func(r *StartRequest) { r.ContextLimit = limit }))
}

var needPattern = regexp.MustCompile(`at least (\d+)`)

func TestCheckTaskContextNamesEveryTaskThatDoesNotFit(t *testing.T) {
	e := newCEnv(t)
	e.usePlan(bigFirstTask)
	err := e.preflight(3000)
	if !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Fatalf("a 6 KB task under a 3000-byte limit: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "task-1") || strings.Contains(msg, "task-2") {
		t.Errorf("the refusal must name task-1 and only task-1: %s", msg)
	}
	if !strings.Contains(msg, "--context-limit") || !strings.Contains(msg, "context_limit") {
		t.Errorf("the refusal must say how to raise the limit: %s", msg)
	}
	if err := e.preflight(0); err != nil {
		t.Fatalf("the default limit must fit a 6 KB task: %v", err)
	}
}

func TestCheckTaskContextMeasuresWhatTheRunWillSend(t *testing.T) {
	e := newCEnv(t)
	e.usePlan(bigFirstTask)
	m := needPattern.FindStringSubmatch(fmtErr(e.preflight(1000)))
	if m == nil {
		t.Fatal("the refusal names no size")
	}
	need, _ := strconv.Atoi(m[1])
	if err := e.preflight(need - 1); !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Fatalf("one byte under the reported need must not fit: %v", err)
	}
	if err := e.preflight(need); err != nil {
		t.Fatalf("the reported need must fit: %v", err)
	}
	// A real run with exactly that limit sends task 1 without pausing, so the
	// check measured the same prompt the run builds.
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ContextLimit = need }))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = id
	e.tick()
	if state, code := e.runState(); state != "running" {
		t.Fatalf("task 1 at the measured limit: %s/%s", state, code)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("%d task prompts sent", got)
	}
}

func TestTheDefaultContextLimitFitsARealTask(t *testing.T) {
	if taskcontext.DefaultMaxEstimatedTokens != 24000 {
		t.Fatalf("default %d", taskcontext.DefaultMaxEstimatedTokens)
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
