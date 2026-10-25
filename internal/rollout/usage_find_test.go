package rollout

import (
	"os"
	"strings"
	"testing"
)

func prompt(text string) string {
	return `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}`
}

// A Codex pane's conversation id may never reach Herdr, so the conversation a
// task ran in is found by its prompt: the task's nonce is in that log and no
// other. A child conversation that was handed the prompt is not the task's.
func TestSessionContainingFindsTheOneRootLogWithTheMarker(t *testing.T) {
	sessions := t.TempDir()
	at := day(2026, 10, 11)
	session(t, sessions, at, "other", "", prompt("another task, nonce aaaa"))
	session(t, sessions, at, "task", "", prompt("do the work, nonce 5301bb2d"), total(10, 0, 1))
	session(t, sessions, at, "child", "task", prompt("do the work, nonce 5301bb2d"))

	got, err := SessionContaining(sessions, "5301bb2d", at, at)
	if err != nil || got != "task" {
		t.Errorf("got %q %v, want task", got, err)
	}
	if got, err := SessionContaining(sessions, "ffff0000", at, at); err != nil || got != "" {
		t.Errorf("no log has the marker: %q %v", got, err)
	}
}

// Two root conversations with one task's nonce cannot be told apart, and a
// guess would count the wrong log, so the answer is an error, not either one.
func TestSessionContainingRefusesToChooseBetweenTwoLogs(t *testing.T) {
	sessions := t.TempDir()
	at := day(2026, 10, 11)
	session(t, sessions, at, "one", "", prompt("nonce 5301bb2d"))
	session(t, sessions, at, "two", "", prompt("nonce 5301bb2d"))
	if got, err := SessionContaining(sessions, "5301bb2d", at, at); err == nil || got != "" {
		t.Errorf("got %q %v, want an error", got, err)
	}
}

// Only logs filed around the run are read, as for descendants, and a marker
// too short to be a nonce is refused rather than matched everywhere.
func TestSessionContainingSearchesOnlyTheRunsDaysAndNeedsARealMarker(t *testing.T) {
	sessions := t.TempDir()
	session(t, sessions, day(2026, 9, 1), "old", "", prompt("nonce 5301bb2d"))
	at := day(2026, 10, 11)
	if got, err := SessionContaining(sessions, "5301bb2d", at, at); err != nil || got != "" {
		t.Errorf("a log filed weeks before the run: %q %v", got, err)
	}
	session(t, sessions, at, "task", "", prompt("nonce 5301bb2d"))
	if got, err := SessionContaining(sessions, "5301", at, at); err == nil || got != "" {
		t.Errorf("a short marker: %q %v", got, err)
	}
}

// A log is read in chunks; a marker that straddles two of them is still found.
func TestSessionContainingFindsAMarkerAcrossAChunkBoundary(t *testing.T) {
	sessions := t.TempDir()
	at := day(2026, 10, 11)
	path := session(t, sessions, at, "task", "")
	head, _ := os.ReadFile(path)
	pad := 1<<16 - len(head) - 4 // the marker starts 4 bytes before the boundary
	body := append(head, []byte(strings.Repeat("x", pad)+"5301bb2d6269\n")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := SessionContaining(sessions, "5301bb2d6269", at, at); err != nil || got != "task" {
		t.Errorf("got %q %v", got, err)
	}
}
