package herdr

import (
	"os"
	"testing"
)

func TestDecodeAgentList(t *testing.T) {
	b, err := os.ReadFile("testdata/agent_list.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	agents, err := decodeList(b)
	if err != nil {
		t.Fatalf("decodeList: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("want at least one agent, got none")
	}
	if agents[0].PaneID == "" {
		t.Error("pane_id did not decode")
	}
}

func TestDecodeErrorEnvelope(t *testing.T) {
	b, err := os.ReadFile("testdata/error.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	e := decodeError(b)
	if e == nil {
		t.Fatal("want an *Error, got nil")
	}
	if e.Code == "" {
		t.Error("error code did not decode")
	}
}

// Schema drift must fail loudly rather than yielding a zero value.
func TestDecodeListRejectsMissingResult(t *testing.T) {
	if _, err := decodeList([]byte(`{"unexpected":true}`)); err == nil {
		t.Fatal("want an error for a response with no result, got nil")
	}
}

func TestDecodeListRejectsGarbage(t *testing.T) {
	if _, err := decodeList([]byte(`not json`)); err == nil {
		t.Fatal("want an error for non-JSON, got nil")
	}
}

// The sandbox denial is NOT a herdr JSON envelope: under Codex's seatbelt the
// CLI dies before it can format one. Anything unparseable means herdr never
// answered, which the scripts must treat as "use the mailbox relay".
func TestNonJSONStderrIsUnreachable(t *testing.T) {
	raw := `Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }`
	if e := decodeError([]byte(raw)); e != nil {
		t.Fatalf("want nil for non-JSON stderr, got %+v", e)
	}
	if !looksUnreachable(raw) {
		t.Error("a PermissionDenied Os error must classify as unreachable")
	}
}

func TestServerNotRunningIsUnreachable(t *testing.T) {
	raw := `{"error":{"code":"server_not_running","message":"no herdr server is running at /x/herdr.sock"}}`
	e := decodeError([]byte(raw))
	if e == nil {
		t.Fatal("want an *Error, got nil")
	}
	if e.Code != "server_not_running" {
		t.Fatalf("want server_not_running, got %q", e.Code)
	}
}

// A herdr that answers and declines is not unreachable: it stays a herdr error,
// so callers do not relay a refused question.
func TestAgentNotFoundIsNotUnreachable(t *testing.T) {
	if looksUnreachable("agent target __x__ not found") {
		t.Error("agent_not_found must not be treated as unreachable")
	}
}

// agent_session is how a reset is proven: its value changes when the CLI
// starts a new conversation. Absent is normal (an integration that has not
// reported) and must read as "", not fail.
func TestAgentSessionID(t *testing.T) {
	agents, err := decodeList([]byte(`{"result":{"agents":[
	  {"pane_id":"p1","agent_status":"idle","agent_session":{"kind":"id","value":"abc"}},
	  {"pane_id":"p2","agent_status":"working"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := agents[0].SessionID(); got != "abc" {
		t.Errorf("SessionID() = %q, want abc", got)
	}
	if got := agents[1].SessionID(); got != "" {
		t.Errorf("absent agent_session: SessionID() = %q, want empty", got)
	}
	if got := agents[1].StatusString(); got != "working" {
		t.Errorf("StatusString() = %q, want working", got)
	}
	var none *Agent
	if none.SessionID() != "" || none.StatusString() != "" {
		t.Error("nil *Agent must read as empty, not panic")
	}
}

// Herdr's agent_session names a pane's own conversation only for Claude Code.
// Copilot's is the launch --session-id, kept after /new. Codex credits every
// conversation to the pane that started its shared daemon. Neither is the
// current conversation.
func TestAgentConversation(t *testing.T) {
	agents, err := decodeList([]byte(`{"result":{"agents":[
	  {"pane_id":"p1","agent":"claude","agent_status":"idle","agent_session":{"kind":"id","value":"abc"}},
	  {"pane_id":"p2","agent":"copilot","agent_status":"idle","agent_session":{"kind":"id","value":"launch-id"}},
	  {"pane_id":"p3","agent":"codex","agent_status":"idle","agent_session":{"kind":"id","value":"another-panes"}},
	  {"pane_id":"p4","agent":"claude","agent_status":"idle"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"abc", "", "", ""} {
		if got := agents[i].Conversation(); got != want {
			t.Errorf("%s Conversation() = %q, want %q", agents[i].Kind, got, want)
		}
	}
	if got := agents[2].SessionID(); got != "another-panes" {
		t.Errorf("codex SessionID() = %q: what Herdr reported must still be readable", got)
	}
	var none *Agent
	if none.Conversation() != "" {
		t.Error("nil *Agent must read as empty, not panic")
	}
}

// No session means no conversation only for Claude and Copilot, whose id
// appears at the first turn. A Codex pane may never get one, and opencode is
// never reported.
func TestAgentHasNoConversation(t *testing.T) {
	agents, err := decodeList([]byte(`{"result":{"agents":[
	  {"pane_id":"p1","agent":"claude","agent_status":"idle"},
	  {"pane_id":"p2","agent":"copilot","agent_status":"idle"},
	  {"pane_id":"p3","agent":"codex","agent_status":"idle"},
	  {"pane_id":"p4","agent":"opencode","agent_status":"idle"},
	  {"pane_id":"p5","agent":"claude","agent_status":"idle","agent_session":{"kind":"id","value":"abc"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, true, false, false, false} {
		if got := agents[i].HasNoConversation(); got != want {
			t.Errorf("%s (pane %s) HasNoConversation() = %v, want %v", agents[i].Kind, agents[i].PaneID, got, want)
		}
	}
}

// state_change_seq counts every lifecycle change Herdr saw for a pane, however
// brief, so a reset is visible between polls.
func TestAgentStateChangeSeq(t *testing.T) {
	b, err := os.ReadFile("testdata/agent_get.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeGet(b)
	if err != nil {
		t.Fatal(err)
	}
	if a.StateChangeSeq != 48 {
		t.Errorf("StateChangeSeq = %d, want 48", a.StateChangeSeq)
	}
}
