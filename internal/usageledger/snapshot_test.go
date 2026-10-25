package usageledger

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestFinalityRequiresTerminalEvidence(t *testing.T) {
	if snapshotComplete(1, 1, 0, false) {
		t.Fatal("silence treated as terminal")
	}
	if snapshotComplete(2, 1, 0, true) {
		t.Fatal("missing role accepted")
	}
	if !snapshotComplete(2, 2, 0, true) {
		t.Fatal("complete snapshot rejected")
	}
	if snapshotComplete(2, 2, 1, true) || snapshotComplete(0, 0, 0, true) {
		t.Fatal("a pending tail or no expected source accepted")
	}
}

// The fixture is testdata/collaboration/full.json. The external token eval
// checks a copy of it: same registry, same logs, same hand-calculated totals.
type fixtureCase struct {
	Registry struct {
		Sources []struct {
			ID       string `json:"source_id"`
			Provider string `json:"provider"`
			Role     string `json:"role"`
			Session  string `json:"session_id"`
			Segments []struct {
				Owner      string `json:"owner"`
				StartAfter string `json:"start_after"`
				EndThrough string `json:"end_through"`
			} `json:"segments"`
			Terminal *struct {
				Through string `json:"through_event"`
			} `json:"terminal"`
		} `json:"sources"`
	} `json:"registry"`
	Logs     map[string][]json.RawMessage `json:"logs"`
	Expected struct {
		Collaboration string          `json:"collaboration"`
		Complete      bool            `json:"complete"`
		Totals        json.RawMessage `json:"totals"`
		Outside       map[string]int  `json:"outside_segments"`
	} `json:"expected"`
}

func loadFixture(t *testing.T) fixtureCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/collaboration/full.json")
	if err != nil {
		t.Fatal(err)
	}
	var c fixtureCase
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// lines renders log records as JSONL, one canonical line each.
func lines(t *testing.T, records []json.RawMessage) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, r := range records {
		v, err := decodeOne(r)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString(canonical(v) + "\n")
	}
	return buf.Bytes()
}

// input builds BuildSnapshot's input from the fixture, parsing each session
// incrementally and keeping each event's latest revision, as the ledger does.
// raw overrides a source's log bytes; a nil entry means its log is missing.
func (c fixtureCase) input(t *testing.T, status string, raw map[string][]byte) SnapshotInput {
	t.Helper()
	in := SnapshotInput{CollaborationID: c.Expected.Collaboration, Status: status}
	var segments []SnapshotSegment
	session := map[string]string{}
	for _, s := range c.Registry.Sources {
		session[s.ID] = s.Session
		for _, g := range s.Segments {
			segments = append(segments, SnapshotSegment{CollaborationID: g.Owner, SourceID: s.ID, StartAfter: g.StartAfter,
				EndThrough: g.EndThrough, Closed: g.EndThrough != ""})
		}
	}
	for _, s := range c.Registry.Sources {
		src := SnapshotSource{ID: s.ID, Provider: s.Provider, Role: s.Role, SessionID: s.Session, Settled: s.Terminal != nil}
		for i := range segments {
			if segments[i].SourceID == s.ID {
				src.Segment = &segments[i]
			}
		}
		in.Sources = append(in.Sources, src)
		body, override := raw[s.ID]
		if !override {
			body = lines(t, c.Logs[s.ID])
		}
		log := SessionLog{Provider: s.Provider, SessionID: s.Session}
		for _, g := range segments {
			if session[g.SourceID] == s.Session {
				log.Segments = append(log.Segments, g)
			}
		}
		if override && body == nil {
			log.Unavailable = "the log was not found"
		} else {
			batch, cur, err := NormalizeIncremental(s.Provider, body, Cursor{})
			if err != nil {
				t.Fatal(err)
			}
			var m merged
			m.add(batch.Events)
			log.Events, log.Cursor, log.Held = m.events(), cur, body[cur.Offset:]
		}
		in.Logs = append(in.Logs, log)
	}
	return in
}

func totalsOf(t *testing.T, s Snapshot) map[string]any {
	t.Helper()
	return generic(t, s.Totals).(map[string]any)
}

func TestSnapshotMatchesTheSharedFixture(t *testing.T) {
	c := loadFixture(t)
	s := BuildSnapshot(c.input(t, "closed", nil))
	if !s.Complete || len(s.Reasons) != 0 {
		t.Fatalf("complete %v, reasons %v", s.Complete, s.Reasons)
	}
	var want any
	json.Unmarshal(c.Expected.Totals, &want)
	if got := totalsOf(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("totals\n got %s\nwant %s", mustJSON(got), c.Expected.Totals)
	}
	for key, n := range c.Expected.Outside {
		if s.SourceBoundaries[key].OutsideSegments != n {
			t.Fatalf("%s: %d events outside segments, want %d", key, s.SourceBoundaries[key].OutsideSegments, n)
		}
	}
}

func TestSnapshotOfAnArchivedButOpenCollaborationIsProvisional(t *testing.T) {
	c := loadFixture(t)
	s := BuildSnapshot(c.input(t, "open", nil))
	if s.Complete || !contains(s.Reasons, "collaboration_open:collab-a") {
		t.Fatalf("an open collaboration was final: %v", s.Reasons)
	}
}

func TestSnapshotPlannerSegmentClosesWithoutProcessExit(t *testing.T) {
	c := loadFixture(t)
	// The planner goes on working after its segment: its last message is mid-turn.
	planner := lines(t, c.Logs["planner"])
	planner = append(planner, []byte(`{"message":{"id":"m6","model":"claude-a","stop_reason":"tool_use","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":1,"output_tokens":1}},"sessionId":"cl-p","type":"assistant","uuid":"u6"}`+"\n")...)
	s := BuildSnapshot(c.input(t, "closed", map[string][]byte{"planner": planner}))
	if !s.Complete {
		t.Fatalf("a closed segment of a planner still working was not final: %v", s.Reasons)
	}
}

func TestSnapshotQuietLogIsNotFinal(t *testing.T) {
	c := loadFixture(t)
	in := c.input(t, "closed", nil)
	for i := range in.Sources {
		if in.Sources[i].ID == "builder" {
			in.Sources[i].Settled = false
		}
	}
	s := BuildSnapshot(in)
	if s.Complete || !contains(s.Reasons, "no_terminal_evidence:builder") || !s.Totals["codex"].Partial {
		t.Fatalf("an unsettled builder was final: %v %+v", s.Reasons, s.Totals["codex"])
	}
}

func TestSnapshotMissingLogIsALowerBound(t *testing.T) {
	c := loadFixture(t)
	s := BuildSnapshot(c.input(t, "closed", map[string][]byte{"reviewer": nil}))
	codex := s.Totals["codex"]
	if s.Complete || !contains(s.Reasons, "missing_source:reviewer") || !codex.Partial || *codex.InputTotal != 175 {
		t.Fatalf("missing reviewer log: %v %+v", s.Reasons, codex)
	}
}

func TestSnapshotOtherCollaborationsSegmentMakesEventsAmbiguous(t *testing.T) {
	c := loadFixture(t)
	in := c.input(t, "closed", nil)
	for i := range in.Logs {
		if in.Logs[i].SessionID == "cl-p" {
			in.Logs[i].Segments = append(in.Logs[i].Segments, SnapshotSegment{CollaborationID: "collab-b", SourceID: "other",
				StartAfter: "m2", EndThrough: "m4", Closed: true})
		}
	}
	s := BuildSnapshot(in)
	if s.Complete || len(s.AmbiguousEvents) != 2 || !contains(s.Reasons, "ambiguous_ownership") {
		t.Fatalf("shared messages: %d ambiguous, %v", len(s.AmbiguousEvents), s.Reasons)
	}
	if got := *s.Totals["claude"].InputTotal; got != 112 {
		t.Fatalf("claude input %d: the ambiguous messages were charged", got)
	}
	if owners := s.AmbiguousEvents[0].Owners; !reflect.DeepEqual(owners, []string{"collab-a", "collab-b"}) {
		t.Fatalf("owners %v", owners)
	}
}

func TestSnapshotPendingTailIsNotFinal(t *testing.T) {
	c := loadFixture(t)
	builder := append(lines(t, c.Logs["builder"]), []byte(`{"type":"event_msg","payload":{"type":"tok`)...)
	s := BuildSnapshot(c.input(t, "closed", map[string][]byte{"builder": builder}))
	if s.Complete || len(s.PendingTails) != 1 || !contains(s.Reasons, "pending_tail:codex:cx-b") {
		t.Fatalf("a pending tail: %v %v", s.PendingTails, s.Reasons)
	}
}

func TestSnapshotUnresolvedAndMismatchedSessions(t *testing.T) {
	c := loadFixture(t)
	in := c.input(t, "closed", nil)
	in.Sources = append(in.Sources, SnapshotSource{ID: "late", Provider: "codex", Role: "builder"})
	for i := range in.Sources {
		if in.Sources[i].ID == "reviewer" {
			in.Sources[i].SessionID = "cx-elsewhere"
		}
	}
	for i := range in.Logs {
		if in.Logs[i].SessionID == "cx-r" {
			in.Logs[i].SessionID = "cx-elsewhere" // the registered session, whose log names cx-r
		}
	}
	s := BuildSnapshot(in)
	if s.Complete || !contains(s.Reasons, "unresolved_identity:late") || !contains(s.Reasons, "session_mismatch:reviewer") {
		t.Fatalf("reasons %v", s.Reasons)
	}
	if len(s.AmbiguousEvents) != 1 || len(s.AmbiguousEvents[0].Owners) != 0 {
		t.Fatalf("a mismatched session's event: %+v", s.AmbiguousEvents)
	}
}
