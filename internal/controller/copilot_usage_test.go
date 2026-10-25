package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

const copilotSession = "9f694d5f-f43c-465e-bf41-93d818e79795"

// copilotUsageLine is one session.usage_record event as Copilot CLI writes it
// to session-state/<id>/events.jsonl, cut to the fields read. inputTokens
// counts every input token, cache reads and writes included.
func copilotUsageLine(t *testing.T, usageID, model string, in, read, write, out int64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "session.usage_record", "id": "e-" + usageID, "timestamp": "2026-10-11T20:58:33.000Z",
		"data": map[string]any{"usage": map[string]any{"model": model, "inputTokens": in, "outputTokens": out,
			"cacheReadTokens": read, "cacheWriteTokens": write, "apiCallId": usageID,
			"accounting": map[string]any{"sourceSessionId": copilotSession, "usageId": usageID}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func copilotUserLine(t *testing.T, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "user.message", "id": "u1", "data": map[string]any{"content": content}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func copilotStart(session string) string {
	return `{"type":"session.start","data":{"sessionId":"` + session + `","copilotVersion":"1.0.96-2"}}`
}

// A Copilot builder's usage is the session.usage_record events in its
// conversation's events.jsonl, one per model call. Input includes the cache.
func TestCopilotStreamReadsEachModelCall(t *testing.T) {
	home := t.TempDir()
	writeLog(t, filepath.Join(home, "session-state", copilotSession, "events.jsonl"),
		copilotStart(copilotSession),
		copilotUserLine(t, "do the task"),
		copilotUsageLine(t, "msg_1", "claude-sonnet-5.5", 32222, 0, 32218, 31),
		`{"type":"assistant.message","data":{"content":"ok"}}`,
		copilotUsageLine(t, "msg_2", "claude-sonnet-5.5", 33000, 32218, 700, 12))

	streams := copilotStreams(home, copilotSession)
	if len(streams) != 1 {
		t.Fatalf("%d streams, want 1: %+v", len(streams), streams)
	}
	s := streams[0]
	if s.Provider != "copilot" || s.SessionID != copilotSession || s.Unavailable != "" || s.Truncated || len(s.Deltas) != 2 {
		t.Fatalf("stream %+v", s)
	}
	if c := s.Deltas[0].Counters; c.Input != 32222 || c.Cached != 0 || c.Output != 31 || s.Deltas[0].Model != "claude-sonnet-5.5" {
		t.Errorf("first call %+v", s.Deltas[0])
	}
	if c := s.Deltas[1].Counters; c.Input != 33000 || c.Cached != 32218 || c.Output != 12 {
		t.Errorf("second call %+v", s.Deltas[1])
	}
	// Reading the log again names every call the same, so nothing is counted twice.
	again := copilotStreams(home, copilotSession)[0]
	if again.Deltas[0].SourceID != s.Deltas[0].SourceID || s.Deltas[0].SourceID == s.Deltas[1].SourceID {
		t.Errorf("source ids are not stable and distinct: %q %q %q", s.Deltas[0].SourceID, s.Deltas[1].SourceID, again.Deltas[0].SourceID)
	}
}

// What cannot be read is said, never counted as zero.
func TestCopilotStreamGaps(t *testing.T) {
	home := t.TempDir()
	// Copilot writes a conversation to disk only at its first turn, so one that
	// has not taken one has no log.
	if s := copilotStreams(home, copilotSession)[0]; s.Unavailable == "" || len(s.Deltas) != 0 {
		t.Errorf("no log: %+v", s)
	}
	if s := copilotStreams(home, "../etc")[0]; s.Unavailable == "" {
		t.Errorf("an id that is not a Copilot one was looked for: %+v", s)
	}
	// A turn whose model call failed writes the prompt and no usage: read, and
	// known to hold none, which is what tells a turn that ended at once.
	writeLog(t, filepath.Join(home, "session-state", copilotSession, "events.jsonl"),
		copilotStart(copilotSession), copilotUserLine(t, "do the task"))
	if s := copilotStreams(home, copilotSession)[0]; !s.NoUsageEvents || s.Unavailable == "" {
		t.Errorf("no usage events: %+v", s)
	}
	// A last line still being written is a lower bound.
	path := filepath.Join(home, "session-state", copilotSession, "events.jsonl")
	body := copilotStart(copilotSession) + "\n" + copilotUsageLine(t, "msg_1", "m", 10, 0, 0, 1) + "\n" + `{"type":"session.usage_rec`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := copilotStreams(home, copilotSession)[0]; !s.Truncated || len(s.Deltas) != 1 {
		t.Errorf("truncated log: %+v", s)
	}
	// A record whose counts cannot be true is left out and said.
	writeLog(t, path, copilotStart(copilotSession), copilotUsageLine(t, "msg_1", "m", 10, 20, 0, 1))
	if s := copilotStreams(home, copilotSession)[0]; s.Rejected != 1 || len(s.Deltas) != 0 {
		t.Errorf("cached above input: %+v", s)
	}
	// A broken line in the middle makes the whole log untrustworthy.
	writeLog(t, path, copilotStart(copilotSession), `{"type":`, copilotUsageLine(t, "msg_1", "m", 10, 0, 0, 1))
	if s := copilotStreams(home, copilotSession)[0]; s.Unavailable == "" || len(s.Deltas) != 0 {
		t.Errorf("broken line: %+v", s)
	}
}

// The task's conversation is the one whose prompt carried its nonce. Herdr's
// Copilot id is the one the pane was launched with, so this is how a task's
// conversation is found when its receipt names none.
func TestCopilotSessionContainingFindsTheTasksConversation(t *testing.T) {
	home := t.TempDir()
	nonce := "4f9c2b7e1d0a4c3b8e6f5a2d1c0b9e8f"
	other := "61550360-3c57-4eb4-97ad-ef47d8a5afd1"
	writeLog(t, filepath.Join(home, "session-state", copilotSession, "events.jsonl"),
		copilotStart(copilotSession), copilotUserLine(t, "task 1. --nonce "+nonce+" and more"))
	writeLog(t, filepath.Join(home, "session-state", other, "events.jsonl"),
		copilotStart(other), copilotUserLine(t, "an unrelated task"),
		// The nonce in something other than a prompt is not the task arriving.
		`{"type":"assistant.message","data":{"content":"I saw `+nonce+` somewhere"}}`)
	now := time.Now()
	got, err := copilotSessionContaining(home, nonce, now.Add(-time.Hour), now)
	if err != nil || got != copilotSession {
		t.Fatalf("got %q, %v; want %s", got, err, copilotSession)
	}
	if got, err := copilotSessionContaining(home, "0000000000000000000000000000dead", now.Add(-time.Hour), now); err != nil || got != "" {
		t.Errorf("an unknown nonce: %q, %v", got, err)
	}
	if _, err := copilotSessionContaining(home, "short", now.Add(-time.Hour), now); err == nil {
		t.Error("a marker too short to name one conversation was searched for")
	}
	// A log last written before the run began cannot hold its task.
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(home, "session-state", copilotSession, "events.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := copilotSessionContaining(home, nonce, now.Add(-time.Hour), now); got != "" {
		t.Errorf("a log older than the run was searched: %q", got)
	}
	// Two conversations that both received the prompt cannot be told apart.
	writeLog(t, filepath.Join(home, "session-state", copilotSession, "events.jsonl"), copilotStart(copilotSession), copilotUserLine(t, nonce))
	writeLog(t, filepath.Join(home, "session-state", other, "events.jsonl"), copilotStart(other), copilotUserLine(t, nonce))
	if got, err := copilotSessionContaining(home, nonce, now.Add(-time.Hour), now); err == nil || got != "" {
		t.Errorf("two conversations: %q, %v; want an error", got, err)
	}
}

// The usage source reads Copilot through the same two calls as Codex: its
// streams, and a search for the conversation that holds a nonce.
func TestAgentUsageSourceReadsCopilot(t *testing.T) {
	home := t.TempDir()
	nonce := "4f9c2b7e1d0a4c3b8e6f5a2d1c0b9e8f"
	writeLog(t, filepath.Join(home, "session-state", copilotSession, "events.jsonl"),
		copilotStart(copilotSession), copilotUserLine(t, "--nonce "+nonce), copilotUsageLine(t, "msg_1", "claude-sonnet-5.5", 100, 40, 0, 5))
	src := AgentUsageSource{CopilotHome: home}
	now := time.Now()
	streams, err := src.Collect(context.Background(), "copilot", copilotSession, now.Add(-time.Hour), now)
	if err != nil || len(streams) != 1 || len(streams[0].Deltas) != 1 {
		t.Fatalf("collect: %+v, %v", streams, err)
	}
	got, err := src.SessionWith(context.Background(), "copilot", nonce, now.Add(-time.Hour), now)
	if err != nil || got != copilotSession {
		t.Errorf("session with: %q, %v", got, err)
	}
	// OpenCode is still unread: its usage is unknown, not zero.
	if _, err := src.Collect(context.Background(), "opencode", "ses_x", now, now); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("opencode: %v", err)
	}
	if !strings.Contains(DefaultAgentUsageSource().CopilotHome, "copilot") {
		t.Errorf("default Copilot home %q", DefaultAgentUsageSource().CopilotHome)
	}
}
