package controller

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

const agySession = "6e1c5ae2-34ee-4c0b-81c1-87f5c1bb7df4"

// --- Minimal protobuf wire encoder for synthetic agy 1.5.14 fixtures ---

func pbVarint(fieldNum uint64, v uint64) []byte {
	var b []byte
	b = binary.AppendUvarint(b, (fieldNum<<3)|0)
	b = binary.AppendUvarint(b, v)
	return b
}

func pbBytes(fieldNum uint64, data []byte) []byte {
	var b []byte
	b = binary.AppendUvarint(b, (fieldNum<<3)|2)
	b = binary.AppendUvarint(b, uint64(len(data)))
	b = append(b, data...)
	return b
}

func pbString(fieldNum uint64, s string) []byte {
	return pbBytes(fieldNum, []byte(s))
}

func pbConcat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func pbMapEntry(fieldNum uint64, k, v string) []byte {
	return pbBytes(fieldNum, pbConcat(pbString(1, k), pbString(2, v)))
}

// agyGenMetaBlob builds one gen_metadata.data protobuf blob matching agy 1.5.14:
// f1 -> f4 (ModelUsage: f2=uncachedIn, f3=out, f5=cachedIn, f7=responseID),
// f19=model, f20={request_id:...}, f21=displayName.
func agyGenMetaBlob(model, displayName, respID, reqID string, uncachedIn, cachedIn, out uint64, useAltOnly bool) []byte {
	var usage []byte
	usage = append(usage, pbVarint(1, 1533)...)
	usage = append(usage, pbVarint(2, uncachedIn)...)
	usage = append(usage, pbVarint(3, out)...)
	if cachedIn > 0 {
		usage = append(usage, pbVarint(5, cachedIn)...)
	}
	if respID != "" {
		usage = append(usage, pbString(7, respID)...)
	}

	var chatMeta []byte
	if useAltOnly {
		chatMeta = append(chatMeta, pbBytes(17, pbBytes(2, usage))...)
	} else {
		chatMeta = append(chatMeta, pbBytes(4, usage)...)
		chatMeta = append(chatMeta, pbBytes(17, pbBytes(2, usage))...)
	}
	if model != "" {
		chatMeta = append(chatMeta, pbString(19, model)...)
	}
	if reqID != "" {
		chatMeta = append(chatMeta, pbMapEntry(20, "request_id", reqID)...)
	}
	if displayName != "" {
		chatMeta = append(chatMeta, pbString(21, displayName)...)
	}
	return pbBytes(1, chatMeta)
}

// agyUserStepBlob builds a step_type 14 (user input) step_payload:
// f1=14, f4=3, f19 -> f2=text, f3 -> f1=text.
func agyUserStepBlob(text string) []byte {
	user := pbConcat(pbString(2, text), pbBytes(3, pbString(1, text)))
	return pbConcat(pbVarint(1, agyStepUserInput), pbVarint(4, 3), pbBytes(19, user))
}

// agyAgentMsgStepBlob builds a step_type 101 (agent/system message) step_payload:
// f1=101, f4=3, f114 -> f1=text.
func agyAgentMsgStepBlob(text string) []byte {
	return pbConcat(pbVarint(1, agyStepAgentMessage), pbVarint(4, 3), pbBytes(114, pbString(1, text)))
}

// agyPlannerStepBlob builds a step_type 15 (planner response) step_payload:
// f1=15, f4=3, f20 -> f1=response, f3=thinking, f8=modified.
func agyPlannerStepBlob(thinking, response, modified string) []byte {
	var planner []byte
	if response != "" {
		planner = append(planner, pbString(1, response)...)
	}
	if thinking != "" {
		planner = append(planner, pbString(3, thinking)...)
	}
	if modified != "" {
		planner = append(planner, pbString(8, modified)...)
	}
	return pbConcat(pbVarint(1, agyStepPlannerResponse), pbVarint(4, 3), pbBytes(20, planner))
}

type agyStepRow struct {
	stepType   int
	stepFormat int
	payload    []byte
}

func writeAgyDB(t *testing.T, home, session string, steps []agyStepRow, genMeta [][]byte) string {
	t.Helper()
	path := agyConversationDB(home, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const schema = `
CREATE TABLE trajectory_meta (trajectory_id text PRIMARY KEY, cascade_id text, trajectory_type integer, source integer);
CREATE TABLE steps (
	idx integer PRIMARY KEY,
	step_type integer NOT NULL DEFAULT 0,
	status integer NOT NULL DEFAULT 0,
	has_subtrajectory numeric NOT NULL DEFAULT false,
	metadata blob,
	error_details blob,
	permissions blob,
	task_details blob,
	render_info blob,
	step_payload blob,
	step_format integer NOT NULL DEFAULT 0
);
CREATE TABLE gen_metadata (idx integer PRIMARY KEY, data blob, size integer NOT NULL DEFAULT 0);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for i, s := range steps {
		if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_payload, step_format) VALUES (?, ?, 3, ?, ?)`,
			i, s.stepType, s.payload, s.stepFormat); err != nil {
			t.Fatal(err)
		}
	}
	for i, g := range genMeta {
		if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)`, i, g, len(g)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// An agy builder's usage is the gen_metadata rows in its conversation SQLite
// database, one per LLM call. agy stores uncached input in f2 and cache reads
// in f5, so total input (SemanticsIncludesCache) is f2 + f5.
func TestAgyStreamReadsEachModelCall(t *testing.T) {
	home := t.TempDir()
	writeAgyDB(t, home, agySession,
		[]agyStepRow{{stepType: agyStepUserInput, payload: agyUserStepBlob("do the task")}},
		[][]byte{
			// Turn 1: no cache reads.
			agyGenMetaBlob("gemini-4-neon", "Gemini 4 Neon (Medium)", "resp_1", "req-1", 12345, 0, 621, false),
			// Turn 2: cache reads exceed uncached input (the exact case that proves f2 excludes cache).
			agyGenMetaBlob("gemini-4-neon", "Gemini 4 Neon (Medium)", "resp_2", "req-2", 5176, 9319, 118, false),
			// Turn 3: fallback to f17.f2, display name f21, and request_id when f4/f19/f7 are absent.
			agyGenMetaBlob("", "Gemini 4 Hydrogen", "", "req-3", 2000, 8000, 250, true),
		})

	streams, err := agyStreams(home, agySession)
	if err != nil {
		t.Fatalf("agyStreams: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("%d streams, want 1: %+v", len(streams), streams)
	}
	s := streams[0]
	if s.Provider != "agy" || s.SessionID != agySession || s.Unavailable != "" || s.Truncated || len(s.Deltas) != 3 {
		t.Fatalf("stream %+v", s)
	}
	if c := s.Deltas[0].Counters; c.Input != 12345 || c.Cached != 0 || c.Output != 621 || s.Deltas[0].Model != "gemini-4-neon" {
		t.Errorf("call 0: %+v", s.Deltas[0])
	}
	if c := s.Deltas[1].Counters; c.Input != 5176+9319 || c.Cached != 9319 || c.Output != 118 {
		t.Errorf("call 1: %+v", s.Deltas[1])
	}
	if c := s.Deltas[2].Counters; c.Input != 10000 || c.Cached != 8000 || c.Output != 250 || s.Deltas[2].Model != "Gemini 4 Hydrogen" {
		t.Errorf("call 2: %+v", s.Deltas[2])
	}
	// Reading again produces identical, distinct source IDs.
	again, err := agyStreams(home, agySession)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Deltas[0].SourceID != s.Deltas[0].SourceID ||
		s.Deltas[0].SourceID == s.Deltas[1].SourceID ||
		s.Deltas[1].SourceID == s.Deltas[2].SourceID {
		t.Errorf("source ids not stable and distinct: %q %q %q %q",
			s.Deltas[0].SourceID, s.Deltas[1].SourceID, s.Deltas[2].SourceID, again[0].Deltas[0].SourceID)
	}
}

// Gaps, corrupt files, and unrecognized agy schema/format versions are handled
// safely and never counted as zero.
func TestAgyStreamGapsAndUnsupportedFormats(t *testing.T) {
	home := t.TempDir()

	// Missing DB.
	if streams, err := agyStreams(home, agySession); err != nil || len(streams) != 1 || streams[0].Unavailable == "" {
		t.Errorf("missing db: %+v, %v", streams, err)
	}
	// Path traversal session id.
	if streams, err := agyStreams(home, "../etc"); err != nil || len(streams) != 1 || streams[0].Unavailable == "" {
		t.Errorf("bad session id: %+v, %v", streams, err)
	}
	// Empty gen_metadata -> NoUsageEvents.
	writeAgyDB(t, home, "empty-gen", []agyStepRow{{stepType: agyStepUserInput, payload: agyUserStepBlob("hi")}}, nil)
	if streams, err := agyStreams(home, "empty-gen"); err != nil || !streams[0].NoUsageEvents || streams[0].Unavailable == "" {
		t.Errorf("empty gen_metadata: %+v, %v", streams, err)
	}
	// Malformed protobuf row is rejected while valid rows still count.
	writeAgyDB(t, home, "bad-row", nil, [][]byte{
		agyGenMetaBlob("gemini-4-neon", "", "r1", "", 100, 0, 10, false),
		{0xff, 0xff, 0xff}, // truncated varint
	})
	if streams, err := agyStreams(home, "bad-row"); err != nil || len(streams[0].Deltas) != 1 || streams[0].Rejected != 1 {
		t.Errorf("bad row: %+v, %v", streams, err)
	}
	// Unrecognized step_format != 0 -> ErrUnsupported.
	writeAgyDB(t, home, "future-fmt",
		[]agyStepRow{{stepType: agyStepPlannerResponse, stepFormat: 2, payload: agyPlannerStepBlob("", "hi", "hi")}},
		[][]byte{agyGenMetaBlob("gemini-4-neon", "", "r1", "", 100, 0, 10, false)})
	if _, err := agyStreams(home, "future-fmt"); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("step_format=2: got err %v, want ErrUnsupported", err)
	}
	// Unrecognized gen_metadata protobuf fields (valid protobuf, no f1.f4) -> ErrUnsupported.
	writeAgyDB(t, home, "future-pb", nil, [][]byte{pbString(99, "new-schema")})
	if _, err := agyStreams(home, "future-pb"); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("future gen_metadata pb: got err %v, want ErrUnsupported", err)
	}
	// Missing expected tables -> ErrUnsupported.
	missingTables := agyConversationDB(home, "future-sql")
	db, err := sql.Open("sqlite", missingTables)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other_table (id integer PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := agyStreams(home, "future-sql"); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("missing tables: got err %v, want ErrUnsupported", err)
	}
	// Non-SQLite corrupt file -> Unavailable (not ErrUnsupported).
	corrupt := agyConversationDB(home, "corrupt-file")
	if err := os.WriteFile(corrupt, []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if streams, err := agyStreams(home, "corrupt-file"); err != nil || len(streams) != 1 || streams[0].Unavailable == "" {
		t.Errorf("corrupt file: %+v, %v", streams, err)
	}
}

// agyReplies returns only visible assistant reply text (step_type 15 f20.f8 /
// f20.f1), excluding thinking (f20.f3), tool-only turns, and user prompts.
func TestAgyRepliesReadsAssistantTextOnly(t *testing.T) {
	home := t.TempDir()
	path := writeAgyDB(t, home, agySession, []agyStepRow{
		{stepType: agyStepUserInput, payload: agyUserStepBlob("user prompt")},
		{stepType: agyStepAgentMessage, payload: agyAgentMsgStepBlob("system msg")},
		// Thinking-only / tool-call turn: no visible reply.
		{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("private reasoning", "", "")},
		// Turn with both response (f1) and modified_response (f8): prefers f8.
		{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("more thought", "raw reply", "final reply")},
		// Turn with only f1 (response).
		{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("", "second reply", "")},
	}, nil)

	got, err := agyReplies(path)
	if err != nil {
		t.Fatalf("agyReplies: %v", err)
	}
	want := "final reply\nsecond reply\n"
	if got != want {
		t.Errorf("agyReplies = %q, want %q", got, want)
	}

	// Unrecognized step_format returns ErrUnsupported.
	badPath := writeAgyDB(t, home, "bad-fmt", []agyStepRow{
		{stepType: agyStepPlannerResponse, stepFormat: 1, payload: agyPlannerStepBlob("", "hi", "hi")},
	}, nil)
	if _, err := agyReplies(badPath); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("step_format=1 replies: got err %v, want ErrUnsupported", err)
	}
}

// agySessionContaining finds the conversation whose user prompt or agent
// message carries the marker, ignoring assistant replies and stale files.
func TestAgySessionContainingFindsTheTasksConversation(t *testing.T) {
	home := t.TempDir()
	nonce := "4f9c2b7e1d0a4c3b8e6f5a2d1c0b9e8f"
	other := "61550360-3c57-4eb4-97ad-ef47d8a5afd1"

	dbPath := writeAgyDB(t, home, agySession, []agyStepRow{
		{stepType: agyStepUserInput, payload: agyUserStepBlob("task 1 --nonce " + nonce)},
	}, nil)
	writeAgyDB(t, home, other, []agyStepRow{
		{stepType: agyStepUserInput, payload: agyUserStepBlob("unrelated task")},
		// Nonce quoted back in an assistant reply must not match.
		{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("", "I saw "+nonce, "I saw "+nonce)},
	}, nil)

	now := time.Now()
	got, err := agySessionContaining(home, nonce, now.Add(-time.Hour), now)
	if err != nil || got != agySession {
		t.Fatalf("got %q, %v; want %s", got, err, agySession)
	}
	if got, err := agySessionContaining(home, "0000000000000000000000000000dead", now.Add(-time.Hour), now); err != nil || got != "" {
		t.Errorf("unknown nonce: %q, %v", got, err)
	}
	if _, err := agySessionContaining(home, "short", now.Add(-time.Hour), now); err == nil {
		t.Error("short marker should fail")
	}

	// Stale DB (older than from - 1 day) is skipped unless its -wal is recent.
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(dbPath, old, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := agySessionContaining(home, nonce, now.Add(-time.Hour), now); got != "" {
		t.Errorf("stale db matched: %q", got)
	}
	// A recent -wal file keeps the conversation in scope.
	if err := os.WriteFile(dbPath+"-wal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := agySessionContaining(home, nonce, now.Add(-time.Hour), now); err != nil || got != agySession {
		t.Errorf("recent -wal should match: got %q, %v", got, err)
	}
	_ = os.Remove(dbPath + "-wal")
	if err := os.Chtimes(dbPath, now, now); err != nil {
		t.Fatal(err)
	}

	// Two matching conversations return an error.
	writeAgyDB(t, home, other, []agyStepRow{
		{stepType: agyStepAgentMessage, payload: agyAgentMsgStepBlob("message " + nonce)},
	}, nil)
	if got, err := agySessionContaining(home, nonce, now.Add(-time.Hour), now); err == nil || got != "" {
		t.Errorf("two conversations: %q, %v; want error", got, err)
	}
}

// AgentUsageSource wires agy through Collect, SessionWith, and AgentReplies.
func TestAgentUsageSourceReadsAgy(t *testing.T) {
	home := t.TempDir()
	nonce := "4f9c2b7e1d0a4c3b8e6f5a2d1c0b9e8f"
	writeAgyDB(t, home, agySession,
		[]agyStepRow{
			{stepType: agyStepUserInput, payload: agyUserStepBlob("--nonce " + nonce)},
			{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("thought", "agy reply", "agy reply")},
		},
		[][]byte{
			agyGenMetaBlob("gemini-4-neon", "", "resp_1", "req-1", 100, 40, 5, false),
		})

	src := AgentUsageSource{AgyHome: home}
	now := time.Now()
	streams, err := src.Collect(context.Background(), "agy", agySession, now.Add(-time.Hour), now)
	if err != nil || len(streams) != 1 || len(streams[0].Deltas) != 1 {
		t.Fatalf("collect: %+v, %v", streams, err)
	}
	if c := streams[0].Deltas[0].Counters; c.Input != 140 || c.Cached != 40 || c.Output != 5 {
		t.Errorf("counters: %+v", c)
	}
	got, err := src.SessionWith(context.Background(), "agy", nonce, now.Add(-time.Hour), now)
	if err != nil || got != agySession {
		t.Errorf("session with: %q, %v", got, err)
	}
	reply, err := src.AgentReplies(context.Background(), "agy", agySession, nonce, now.Add(-time.Hour), now)
	if err != nil || reply != "agy reply\n" {
		t.Errorf("agent replies by session: %q, %v", reply, err)
	}
	replyByMarker, err := src.AgentReplies(context.Background(), "agy", "", nonce, now.Add(-time.Hour), now)
	if err != nil || replyByMarker != "agy reply\n" {
		t.Errorf("agent replies by marker: %q, %v", replyByMarker, err)
	}
	if !strings.Contains(DefaultAgentUsageSource().AgyHome, "antigravity-cli") {
		t.Errorf("default Agy home %q", DefaultAgentUsageSource().AgyHome)
	}
}
