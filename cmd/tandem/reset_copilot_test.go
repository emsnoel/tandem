package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The lines Copilot CLI wrote around a /new, from a real process log: the old
// conversation is unregistered and the new one registered, before the new one
// has a session directory.
const copilotLog = `2026-10-11T20:56:07.935Z [INFO] [rust:otel] OTel session policy applied
2026-10-11T20:57:37.236Z [INFO] Unregistering foreground session: fad08507-312c-414d-9b8f-a6a1c553a934
2026-10-11T20:57:37.240Z [INFO] Registering foreground session: 61550360-3c57-4eb4-97ad-ef47d8a5afd1
2026-10-11T20:58:19.924Z [INFO] Unregistering foreground session: 61550360-3c57-4eb4-97ad-ef47d8a5afd1
2026-10-11T20:58:19.926Z [INFO] Registering foreground session: 9f694d5f-f43c-465e-bf41-93d818e79795
2026-10-11T20:58:34.185Z [INFO] Session named: "Ready Response Confirmation"
`

func writeCopilotLog(t *testing.T, name, body string) string {
	t.Helper()
	logs := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(logs)
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The conversation a /new opened is the last one registered at or after the
// command was typed. One registered before it is an earlier reset's.
func TestCopilotLogNamesTheConversationOpenedSince(t *testing.T) {
	home := writeCopilotLog(t, "process-1791752166515-41843.log", copilotLog)
	for _, c := range []struct{ since, want string }{
		{"2026-10-11T20:57:30Z", "9f694d5f-f43c-465e-bf41-93d818e79795"},
		{"2026-10-11T20:58:19.900Z", "9f694d5f-f43c-465e-bf41-93d818e79795"},
		{"2026-10-11T20:58:20Z", ""},
	} {
		if got := copilotLogSession(home, 41843, at(t, c.since)); got != c.want {
			t.Errorf("since %s: got %q, want %q", c.since, got, c.want)
		}
	}
}

// Every Copilot process writes its own log, named by its pid. Another pane's
// /new is never taken for this one's.
func TestCopilotLogIsThePanesOwn(t *testing.T) {
	home := writeCopilotLog(t, "process-1791752166515-41843.log", copilotLog)
	if got := copilotLogSession(home, 4184, at(t, "2026-10-11T20:57:30Z")); got != "" {
		t.Errorf("pid 4184 read pid 41843's log: %q", got)
	}
	if got := copilotLogSession(filepath.Join(home, "missing"), 41843, at(t, "2026-10-11T20:57:30Z")); got != "" {
		t.Errorf("no log dir: %q", got)
	}
}

// A line not in the exact expected format proves nothing.
func TestCopilotLogIgnoresOtherLines(t *testing.T) {
	home := writeCopilotLog(t, "process-1-7.log", `not-a-time [INFO] Registering foreground session: x
2026-10-11T20:58:19.926Z [INFO] Registering foreground session:
2026-10-11T20:58:19.926Z [INFO] Registering foreground session: ../../etc
2026-10-11T20:58:19.926Z [WARNING] Registering foreground session: abc
`)
	if got := copilotLogSession(home, 7, at(t, "2026-10-11T00:00:00Z")); got != "" {
		t.Errorf("got %q from lines that are not a registration", got)
	}
}

// Herdr names the pane's processes; the Copilot one is the CLI, not an MCP
// server it started.
func TestCopilotPidIsTheCLIsProcess(t *testing.T) {
	raw := []byte(`{"id":"x","result":{"process_info":{"foreground_process_group_id":41843,"shell_pid":41455,"pane_id":"w8:p3D","foreground_processes":[
	  {"argv0":"codebase-memory-mcp","name":"codebase-memory","pid":52803},
	  {"argv0":"copilot","name":"copilot","pid":41843}]},"type":"pane_process_info"}}`)
	info, err := decodeProcessInfo(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.pidOf("copilot"); got != 41843 {
		t.Errorf("pidOf(copilot) = %d, want 41843", got)
	}
	if got := info.pidOf("codex"); got != 0 {
		t.Errorf("pidOf(codex) = %d, want 0", got)
	}
}
