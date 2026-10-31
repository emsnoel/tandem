package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestRecordBuilderWritesAndPrints(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","autonomy":"auto"}}`, "")
	relay := filepath.Join(t.TempDir(), "relay")
	out := captureStdout(t, func() int {
		return cmdConfig([]string{"record-builder", "--relay", relay, "--root", dir, "--adopted", "codex"})
	})
	got := fields(out)
	want := `{"kind":"codex","autonomy":"auto","adopted":true}`
	if len(got) != 3 || got[0] != "codex" || got[1] != "auto" || got[2] != want {
		t.Fatalf("got %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(relay, config.BuilderFile)); err != nil || string(b) != want+"\n" {
		t.Fatalf("file %q, %v", b, err)
	}
}

func TestRecordBuilderDryRunWritesNothing(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","autonomy":"auto"}}`, "")
	relay := filepath.Join(t.TempDir(), "relay")
	out := captureStdout(t, func() int {
		return cmdConfig([]string{"record-builder", "--relay", relay, "--root", dir, "--dry-run"})
	})
	if got := fields(out); len(got) != 3 || got[2] != `{"kind":"codex","autonomy":"auto","adopted":false}` {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(relay); !os.IsNotExist(err) {
		t.Fatalf("dry run created the relay: %v", err)
	}
}

// Review focus 5: an empty kind or autonomy refuses instead of writing a
// record the gate would refuse every review on.
func TestRecordBuilderRefusesEmptyKind(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","autonomy":"auto"}}`, "")
	relay := filepath.Join(t.TempDir(), "relay")
	out, rc := captureStdoutRC(t, func() int {
		return cmdConfig([]string{"record-builder", "--relay", relay, "--root", dir, "--kind", " "})
	})
	if rc != exitcode.Internal || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
	if _, err := os.Stat(filepath.Join(relay, config.BuilderFile)); !os.IsNotExist(err) {
		t.Fatalf("wrote a record: %v", err)
	}
}

func TestReviewerRecordPrintsModelAndJSON(t *testing.T) {
	relay := t.TempDir()
	if err := config.WriteReviewer(relay, config.ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus", Autonomy: "auto", ModelMeasured: true}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int {
		return cmdReviewer([]string{"record", "--relay", relay, "--kind", "claude", "--name", "reviewer", "--autonomy", "ask"})
	})
	want := `{"kind":"claude","name":"reviewer","model":"opus","autonomy":"ask","model_measured":true}`
	if got := fields(out); len(got) != 2 || got[0] != "opus" || got[1] != want {
		t.Fatalf("got %q", got)
	}
	out = captureStdout(t, func() int {
		return cmdReviewer([]string{"record", "--relay", relay, "--kind", "codex", "--name", "reviewer",
			"--autonomy", "auto", "--model", "gpt", "--started", "--dry-run"})
	})
	if got := fields(out); got[0] != "gpt" {
		t.Fatalf("got %q", got)
	}
	if rec, _ := config.LoadReviewer(relay); rec.Kind != "claude" || rec.Autonomy != "ask" {
		t.Fatalf("dry run changed the record: %+v", rec)
	}
}
