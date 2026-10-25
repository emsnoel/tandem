package main

import (
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestBuildBudgetPersistsLimitsAndReportsTheDecision(t *testing.T) {
	e, _ := startedCLI(t)
	run := func(args ...string) (int, map[string]any, string) {
		rc, got, _, errOut := e.run3(append([]string{"build", "budget", "--root", e.root, "--state-home", e.home}, args...)...)
		return rc, got, errOut
	}
	sessions := t.TempDir()
	codexLog(t, sessions, "cli", "", totalEv(900, 0, 5))

	rc, got, errOut := run("--scope", "run", "--limit", "1000", "--sessions-dir", sessions)
	if rc != exitcode.OK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	d := got["decision"].(map[string]any)
	if got["scope"] != "run" || got["limit"] != float64(1000) || d["state"] != "warning" || d["scheduling_allowed"] != true {
		t.Errorf("%v", got)
	}
	// The scope defaults to the run.
	rc, got, _ = run("--limit", "800", "--sessions-dir", sessions)
	if rc != exitcode.OK || got["scope"] != "run" || got["decision"].(map[string]any)["state"] != "exhausted" || got["decision"].(map[string]any)["scheduling_allowed"] != false {
		t.Errorf("%v", got)
	}
	// A task limit is its own.
	rc, got, _ = run("--scope", "task", "--limit", "5000", "--sessions-dir", sessions)
	if rc != exitcode.OK || got["scope"] != "task" || got["limit"] != float64(5000) {
		t.Errorf("%v", got)
	}
	// The limits are on the run: status reads them back.
	_, st, _, _ := e.run3("build", "status", "--root", e.root, "--state-home", e.home, "--json")
	if st["task_input_limit"] != float64(5000) || st["run_input_limit"] != float64(800) {
		t.Errorf("status %v", st)
	}
	// Zero removes a limit.
	if rc, got, _ := run("--scope", "run", "--limit", "0", "--sessions-dir", sessions); rc != exitcode.OK || got["limit"] != float64(0) {
		t.Errorf("%v", got)
	}
}

func TestBuildBudgetParsesStrictly(t *testing.T) {
	e, _ := startedCLI(t)
	for name, args := range map[string][]string{
		"no limit":      {"--scope", "run"},
		"negative":      {"--limit", "-5"},
		"not a number":  {"--limit", "lots"},
		"a decimal":     {"--limit", "1.5"},
		"a sign":        {"--limit", "+5"},
		"spaces":        {"--limit", " 5"},
		"too large":     {"--limit", "99999999999999999999"},
		"unknown scope": {"--scope", "everything", "--limit", "5"},
		"unknown flag":  {"--limit", "5", "--soft"},
		"an extra word": {"--limit", "5", "run"},
	} {
		rc, _, _, _ := e.run3(append([]string{"build", "budget", "--root", e.root, "--state-home", e.home}, args...)...)
		if rc != exitcode.Usage {
			t.Errorf("%s: rc %d", name, rc)
		}
	}
	// A run id that is not the active run is refused, and changes nothing.
	rc, _, _, errOut := e.run3("build", "budget", "--root", e.root, "--state-home", e.home, "--run", strings.Repeat("1", 32), "--limit", "5")
	if rc == exitcode.OK || !strings.Contains(errOut, "active") {
		t.Errorf("rc %d %q", rc, errOut)
	}
	// Without a durable run there is nothing to limit.
	legacy := newCLI(t)
	if rc, _, _, errOut := legacy.run3("build", "budget", "--root", legacy.root, "--limit", "5"); rc == exitcode.OK || !strings.Contains(errOut, "durable") {
		t.Errorf("rc %d %q", rc, errOut)
	}
}
