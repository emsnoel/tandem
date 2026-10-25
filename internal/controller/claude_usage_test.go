package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/rollout"
)

const claudeSession = "57312883-6ae0-44d2-8053-3429d17e95c0"

// claudeLine is one assistant record of a Claude Code conversation log.
func claudeLine(t *testing.T, uuid, msgID, model, stop string, in, write, read, out int64) string {
	t.Helper()
	msg := map[string]any{"id": msgID, "model": model,
		"usage": map[string]any{"input_tokens": in, "cache_creation_input_tokens": write, "cache_read_input_tokens": read, "output_tokens": out}}
	if stop != "" {
		msg["stop_reason"] = stop
	}
	b, err := json.Marshal(map[string]any{"type": "assistant", "uuid": uuid, "sessionId": claudeSession, "message": msg})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Claude builder's usage is in its conversation's log under the projects
// directory, and each subagent it ran logs separately under the session.
// Input counts every input token, cache reads and writes included; cached is
// the cache reads.
func TestClaudeStreamsReadTheSessionAndItsSubagents(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "-Users-you-projects-tandem")
	writeLog(t, filepath.Join(repo, claudeSession+".jsonl"),
		`{"type":"user","sessionId":"`+claudeSession+`","message":{"role":"user","content":"do the task"}}`,
		// One message written as two records: the latest revision counts.
		claudeLine(t, "u1", "m1", "claude-sonnet-5-5", "", 2, 100, 1000, 10),
		claudeLine(t, "u2", "m1", "claude-sonnet-5-5", "tool_use", 2, 100, 1000, 40),
		claudeLine(t, "u3", "m2", "claude-sonnet-5-5", "end_turn", 3, 0, 1100, 7))
	writeLog(t, filepath.Join(repo, claudeSession, "subagents", "agent-a1.jsonl"),
		claudeLine(t, "s1", "m9", "claude-haiku-5-5", "end_turn", 5, 50, 0, 3))

	streams := claudeStreams(dir, claudeSession)
	if len(streams) != 2 {
		t.Fatalf("%d streams, want the session and its subagent: %+v", len(streams), streams)
	}
	main, sub := streams[0], streams[1]
	if main.Provider != "claude" || main.SessionID != claudeSession || main.Unavailable != "" || main.Truncated {
		t.Fatalf("session stream %+v", main)
	}
	if len(main.Deltas) != 2 {
		t.Fatalf("%d deltas, want one per message: %+v", len(main.Deltas), main.Deltas)
	}
	if got := main.Deltas[0].Counters; got != (rollout.Counters{Input: 1102, Cached: 1000, Output: 40}) {
		t.Errorf("first message %+v, want its latest revision", got)
	}
	if got := main.Deltas[1].Counters; got != (rollout.Counters{Input: 1103, Cached: 1100, Output: 7}) {
		t.Errorf("second message %+v", got)
	}
	if main.Deltas[0].Model != "claude-sonnet-5-5" {
		t.Errorf("model %q", main.Deltas[0].Model)
	}
	if main.Deltas[0].SourceID == main.Deltas[1].SourceID || len(main.Deltas[0].SourceID) != 32 {
		t.Errorf("source ids %q %q are not distinct 32-hex ids", main.Deltas[0].SourceID, main.Deltas[1].SourceID)
	}
	if sub.ParentID != claudeSession || sub.SessionID == claudeSession || len(sub.Deltas) != 1 ||
		sub.Deltas[0].Counters != (rollout.Counters{Input: 55, Cached: 0, Output: 3}) || sub.Deltas[0].Model != "claude-haiku-5-5" {
		t.Errorf("subagent stream %+v", sub)
	}
	// Reading the same logs again gives the same ids, so nothing is counted twice.
	again := claudeStreams(dir, claudeSession)
	if again[0].Deltas[1].SourceID != main.Deltas[1].SourceID {
		t.Errorf("source ids change between reads")
	}
}

func TestClaudeStreamsMissingLogIsAGap(t *testing.T) {
	streams := claudeStreams(t.TempDir(), claudeSession)
	if len(streams) != 1 || streams[0].Unavailable == "" || len(streams[0].Deltas) != 0 {
		t.Fatalf("%+v", streams)
	}
}

// A session id names a file, so one that is not a plain id is never used in a
// path.
func TestClaudeStreamsRefuseASessionThatIsNotAnID(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "x", "secret.jsonl"), claudeLine(t, "u", "m", "x", "end_turn", 1, 0, 0, 1))
	for _, bad := range []string{"", "../x/secret", "*", "a/b"} {
		streams := claudeStreams(filepath.Join(dir, "projects"), bad)
		if len(streams) != 1 || streams[0].Unavailable == "" || len(streams[0].Deltas) != 0 {
			t.Errorf("%q: %+v", bad, streams)
		}
	}
}

// A conversation whose last message has not finished is still being written.
// That message's figures can still change and a stored record cannot, so it is
// left out until it is final, and the stream is a lower bound meanwhile.
func TestClaudeStreamsUnfinishedLogIsPartial(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "-repo", claudeSession+".jsonl"),
		claudeLine(t, "u1", "m0", "claude-sonnet-5-5", "tool_use", 1, 0, 0, 1),
		claudeLine(t, "u2", "m1", "claude-sonnet-5-5", "", 2, 100, 1000, 10))
	s := claudeStreams(dir, claudeSession)[0]
	if !s.Truncated || len(s.Deltas) != 1 || s.Deltas[0].Counters.Output != 1 || len(s.Diagnostics) == 0 {
		t.Fatalf("%+v", s)
	}
}

// A message's source id is its message id, not its line: its final revision
// can be on any line, and a log read again must give the same ids, so a stored
// record is matched, never added to.
func TestClaudeSourceIDsFollowTheMessageNotTheLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "-repo", claudeSession+".jsonl")
	writeLog(t, path, claudeLine(t, "u2", "m1", "claude-sonnet-5-5", "end_turn", 2, 100, 1000, 40))
	before := claudeStreams(dir, claudeSession)[0].Deltas[0].SourceID
	writeLog(t, path, `{"type":"user","message":{"content":"x"}}`, claudeLine(t, "u2", "m1", "claude-sonnet-5-5", "end_turn", 2, 100, 1000, 40))
	after := claudeStreams(dir, claudeSession)[0].Deltas
	if len(after) != 1 || after[0].SourceID != before || after[0].Counters.Output != 40 {
		t.Errorf("before %q, after %+v", before, after)
	}
}
