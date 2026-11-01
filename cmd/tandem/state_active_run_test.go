package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestStateActiveRunPrintsID(t *testing.T) {
	d := durableRepo(t)
	out, rc := captureStdoutRC(t, func() int { return cmdState([]string{"active-run", "--relay", d.relay}) })
	if rc != exitcode.OK || out != d.run+"\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestStateActiveRunNoMarker(t *testing.T) {
	out, rc := captureStdoutRC(t, func() int { return cmdState([]string{"active-run", "--relay", t.TempDir()}) })
	if rc != exitcode.OK || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestStateActiveRunCorrupt(t *testing.T) {
	relay := t.TempDir()
	if err := os.WriteFile(filepath.Join(relay, controller.ActiveRunFile), []byte(`{"run_id":`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, rc := captureStdoutRC(t, func() int { return cmdState([]string{"active-run", "--relay", relay}) })
	if rc != exitcode.ManualGate || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}
