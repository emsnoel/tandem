package main

import (
	"os"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

const idPlan = "# Plan\n\n**Goal:** g.\n\n### Task 1: First\nTask-ID: progress-repo\n\nfirst\n\n" +
	"### Task 2: Second\nTask-ID: task-1\n\nsecond\n"

// newIDCLI is newCLI with a plan whose tasks carry their own Task-ID lines.
// Task 2's id is task-1, so "task-1" names two tasks.
func newIDCLI(t *testing.T) *cliEnv {
	t.Helper()
	e := newCLI(t)
	os.WriteFile(e.plan, []byte(idPlan), 0o644)
	if out, err := gitCmd(e.root, "commit", "-qam", "ids"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if rc, _, errOut := e.start(); rc != exitcode.OK {
		t.Fatalf("start: rc %d %s", rc, errOut)
	}
	return e
}

// A stop message prints the task as its plan Task-ID; accept must take it.
func TestDurableAcceptTakesThePlansTaskID(t *testing.T) {
	e := newIDCLI(t)
	_, _, byNumber := e.sub("accept", "--task", "1", "--waive", "review", "--because", "I read it")
	rc, _, byID := e.sub("accept", "--task", "progress-repo", "--waive", "review", "--because", "I read it")
	if rc == exitcode.Usage || byID != byNumber {
		t.Fatalf("--task progress-repo: rc %d %q; want what --task 1 says: %q", rc, byID, byNumber)
	}
}

func TestDurableAcceptRefusesAnUnknownOrAmbiguousTask(t *testing.T) {
	e := newIDCLI(t)
	for ref, want := range map[string]string{
		"nonesuch": `no task "nonesuch"`,
		"3":        `no task "3"`,
		"task-1":   `"task-1" names more than one task`,
	} {
		rc, _, errOut := e.sub("accept", "--task", ref, "--waive", "review", "--because", "x")
		if rc != exitcode.Usage || !strings.Contains(errOut, want) || !strings.Contains(errOut, "1 (progress-repo), 2 (task-1)") {
			t.Errorf("--task %s: rc %d %q; want usage naming %q and the valid tasks", ref, rc, errOut, want)
		}
	}
}
