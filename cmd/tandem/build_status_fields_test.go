package main

import (
	"context"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func pausedRun(t *testing.T, code string) *durable {
	t.Helper()
	d := durableRepo(t)
	open, err := openDurable(d.root, d.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer open.close()
	if err := open.st.SetRunPausedFor(context.Background(), d.run, code, "", ""); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestStatusFieldsAppendTaskAndAgents(t *testing.T) {
	d := pausedRun(t, "no_reviewer")
	out, rc := captureStdoutRC(t, func() int {
		return cmdBuild([]string{"status", "--root", d.root, "--state-home", d.home, "--fields"})
	})
	got := fields(out)
	if rc != exitcode.OK || len(got) != 7 {
		t.Fatalf("rc %d, %d fields %q, want 7", rc, len(got), got)
	}
	if got[0] != "paused" || got[1] != "no_reviewer" {
		t.Errorf("state and pause_code moved: %q", got)
	}
	if got[4] != "receipt-store" || got[5] != "builder" || got[6] != "reviewer" {
		t.Errorf("task, builder, reviewer = %q %q %q", got[4], got[5], got[6])
	}
}

func TestStatusReasonHoldsOnlyFacts(t *testing.T) {
	d := pausedRun(t, "no_reviewer")
	rc, out, _ := runCapture(t, "build", "status", "--root", d.root, "--state-home", d.home, "--json")
	if rc != exitcode.OK {
		t.Fatalf("rc %d", rc)
	}
	if r, _ := oneJSON(t, out)["pause_reason"].(string); r != "" {
		t.Fatalf("a no_reviewer pause has no run-specific facts, got %q", r)
	}
	_, human, _ := runCapture(t, "build", "status", "--root", d.root, "--state-home", d.home)
	if strings.Contains(human, "reason:") || strings.Contains(human, "tandem-pause-note") {
		t.Fatalf("no facts, so no reason line or pointer:\n%s", human)
	}
}

func TestBuildPauseCodesPrintsEachCodeNULTerminated(t *testing.T) {
	out, rc := captureStdoutRC(t, func() int { return cmdBuild([]string{"pause-codes"}) })
	got := fields(out)
	if rc != exitcode.OK || strings.Join(got, " ") != strings.Join(controller.PauseCodes(), " ") || !strings.HasSuffix(out, "\x00") {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}
