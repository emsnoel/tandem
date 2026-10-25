package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/taskcontext"
)

func TestTaskContextExplainReadsThePersistedManifest(t *testing.T) {
	e, a := startedCLI(t)
	rc, got, _, errOut := e.run3("task", "context", "--attempt", a.attempt, "--explain", "--json", "--root", e.root, "--state-home", e.home)
	if rc != exitcode.OK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	if got["attempt_id"] != a.attempt || got["task_id"] != "task-1" || got["estimate_method"] != "utf8-bytes-upper-estimate-v1" || got["limit"] != float64(taskcontext.DefaultMaxEstimatedTokens) ||
		len(got["prompt_hash"].(string)) != 64 || len(got["manifest_hash"].(string)) != 64 {
		t.Errorf("%v", got)
	}
	est := got["estimated"].(map[string]any)
	if est["total"].(float64) == 0 || est["total"].(float64) != est["required"].(float64)+est["optional"].(float64) {
		t.Errorf("%v", est)
	}
	if _, ok := got["selected"].([]any); !ok {
		t.Errorf("selected must be a list, even when empty: %v", got["selected"])
	}

	rc, out, _ := e.run("task", "context", "--attempt", a.attempt, "--explain", "--root", e.root, "--state-home", e.home)
	for _, want := range []string{"attempt " + a.attempt, "task-1", fmt.Sprintf("limit %d", taskcontext.DefaultMaxEstimatedTokens), "utf8-bytes-upper-estimate-v1", "required", "optional", "total", "selected (0)", "omitted (0)"} {
		if rc != exitcode.OK || !strings.Contains(out, want) {
			t.Errorf("the text is missing %q (rc %d):\n%s", want, rc, out)
		}
	}
	// Bad arguments, and no model is involved anywhere.
	for name, args := range map[string][]string{
		"without --explain": {"task", "context", "--attempt", a.attempt},
		"no attempt":        {"task", "context", "--explain"},
		"no subverb":        {"task"},
		"unknown subverb":   {"task", "frobnicate"},
	} {
		if rc, _, _ := e.run(append(args, "--root", e.root, "--state-home", e.home)...); rc != exitcode.Usage {
			t.Errorf("%s: rc %d", name, rc)
		}
	}
	if rc, out, errOut := e.run("task", "context", "--attempt", strings.Repeat("0", 32), "--explain", "--root", e.root, "--state-home", e.home); rc == exitcode.OK || out != "" {
		t.Errorf("an unknown attempt: rc %d %q %q", rc, out, errOut)
	}
}
