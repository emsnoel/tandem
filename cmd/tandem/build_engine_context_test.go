package main

import (
	"os"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

func TestADurableStartRefusesATaskTooLargeForItsLimitAndCreatesNothing(t *testing.T) {
	for _, dry := range []bool{false, true} {
		e := newCLI(t)
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		extra := []string{"--context-limit", "500"}
		if dry {
			extra = append(extra, "--dry-run")
		}
		rc, _, errOut := e.start(extra...)
		if rc != exitcode.ManualGate || !strings.Contains(errOut, "task-1 needs") || !strings.Contains(errOut, "no run was started") {
			t.Fatalf("dry %v: rc %d %s", dry, rc, errOut)
		}
		if ar, _ := controller.ReadActiveRun(paths.Relay(e.root)); ar != nil {
			t.Fatalf("dry %v: a refused start wrote a marker", dry)
		}
		if _, err := os.Stat(e.home); !os.IsNotExist(err) {
			t.Fatalf("dry %v: a refused start created the state home", dry)
		}
		if e.tr.count() != 0 {
			t.Fatalf("dry %v: a prompt was sent", dry)
		}
	}
	// A bad limit is a usage error.
	e := newCLI(t)
	if rc, _, _ := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder-kind", "codex", "--context-limit", "-5", "--state-home", e.home); rc != exitcode.Usage {
		t.Errorf("a negative limit: rc %d", rc)
	}
}

func TestADryRunContinueOfADurableRunSendsNothing(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, got, errOut := e.start()
	if rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	rc, out, errOut := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder-kind", "codex", "--reviewer", "reviewer", "--reviewer-kind", "claude", "--state-home", e.home, "--resume-only", "--dry-run")
	if rc != exitcode.OK {
		t.Fatalf("dry continue: rc %d %s", rc, errOut)
	}
	if v := oneJSON(t, out); v["dry_run"] != true || v["run_id"] != got["run_id"] {
		t.Errorf("a dry continue must name the run it would continue: %v", v)
	}
	if n := e.tr.count(); n != 0 {
		t.Fatalf("a dry continue sent %d prompts", n)
	}
}
