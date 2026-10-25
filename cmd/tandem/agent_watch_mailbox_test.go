package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/state"
)

func relayFixture(t *testing.T) string {
	t.Helper()
	relay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(relay, "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	return relay
}

func writeMarker(t *testing.T, relay, run string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(controller.RunDir(relay, run), "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"engine_version":2,"protocol_version":2,"run_id":"` + run + `","project_id":"` + strings.Repeat("b", 32) + `"}`
	if err := os.WriteFile(filepath.Join(relay, controller.ActiveRunFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func arm(t *testing.T, relay, run, stateRoot string) string {
	t.Helper()
	mw, ready, err := openMailbox(relay, run, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	mw.Close()
	return ready
}

// The bridge chose its no-run branch, then a run started: wake now, so the
// next pass ticks the run, instead of after the backstop.
func TestOpenMailboxWakesForARunTheCallerDidNotExpect(t *testing.T) {
	relay := relayFixture(t)
	run := strings.Repeat("a", 32)
	writeMarker(t, relay, run)
	want := "run\t" + filepath.Join(relay, controller.ActiveRunFile)
	if got := arm(t, relay, "", ""); got != want {
		t.Fatalf("no run expected: got %q want %q", got, want)
	}
	other := strings.Repeat("c", 32)
	if err := os.MkdirAll(filepath.Join(controller.RunDir(relay, other), "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := arm(t, relay, other, t.TempDir()); got != want {
		t.Fatalf("another run expected: got %q want %q", got, want)
	}
	if got := arm(t, relay, run, t.TempDir()); got != "" {
		t.Fatalf("the expected run: got %q", got)
	}
}

// engine_guard deals with a marker this tandem cannot read. Waking on it
// would make the bridge spin.
func TestOpenMailboxDoesNotWakeForAnUnreadableMarker(t *testing.T) {
	relay := relayFixture(t)
	if err := os.WriteFile(filepath.Join(relay, controller.ActiveRunFile), []byte(`{"protocol_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := arm(t, relay, "", ""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenMailboxWakesForAMessageAlreadyQueued(t *testing.T) {
	relay := relayFixture(t)
	run := strings.Repeat("a", 32)
	writeMarker(t, relay, run)
	msg := filepath.Join(controller.RunDir(relay, run), "q", "e1.json")
	if err := os.WriteFile(msg, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := arm(t, relay, run, t.TempDir()); got != "queued\t"+msg {
		t.Fatalf("got %q", got)
	}
}

func TestOpenMailboxWatchesTheNudge(t *testing.T) {
	relay := relayFixture(t)
	run := strings.Repeat("a", 32)
	writeMarker(t, relay, run)
	stateRoot := t.TempDir()
	mw, ready, err := openMailbox(relay, run, stateRoot)
	if err != nil || ready != "" {
		t.Fatalf("ready %q err %v", ready, err)
	}
	defer mw.Close()
	nudge := filepath.Join(stateRoot, state.NudgeFile)
	if err := os.WriteFile(nudge, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case wk := <-mw.Wakes():
		if wk.Kind != "nudge" || wk.Path != nudge {
			t.Fatalf("wake %+v", wk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no wake for the nudge")
	}
}

func TestOpenMailboxFailsWithoutTheRunsQueue(t *testing.T) {
	relay := relayFixture(t)
	if _, _, err := openMailbox(relay, strings.Repeat("a", 32), t.TempDir()); err == nil {
		t.Fatal("armed a run whose queue folder does not exist")
	}
}
