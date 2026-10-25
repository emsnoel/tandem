package usageledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// merged folds incremental batches the way the ledger stores them: an event's
// latest revision replaces earlier ones, in first-seen order.
type merged struct {
	order  []string
	latest map[string]Event
}

func (m *merged) add(events []Event) {
	if m.latest == nil {
		m.latest = map[string]Event{}
	}
	for _, e := range events {
		if old, ok := m.latest[e.EventID]; !ok {
			m.order = append(m.order, e.EventID)
		} else if old.Revision > e.Revision {
			continue
		}
		m.latest[e.EventID] = e
	}
}

func (m *merged) events() []Event {
	out := []Event{}
	for _, id := range m.order {
		out = append(out, m.latest[id])
	}
	return out
}

// persisted is a cursor after the JSON round trip a commit puts it through.
func persisted(t *testing.T, c Cursor) Cursor {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var out Cursor
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every golden, split at every byte, parsed in two calls with the cursor
// committed between them, gives what one whole read gives: the same latest
// events and the same reasons. A split inside a JSON record is held, never
// misread, and a Codex total in the second chunk is differenced from the first.
func TestIncrementalMatchesWholeReadAtEverySplit(t *testing.T) {
	for _, g := range loadGoldens(t) {
		raw := g.jsonl(t)
		whole, err := Normalize(g.Provider, raw)
		if err != nil {
			t.Fatal(err)
		}
		for k := 0; k <= len(raw); k++ {
			first, cur, err := NormalizeIncremental(g.Provider, raw[:k], Cursor{})
			if err != nil {
				t.Fatal(err)
			}
			cur = persisted(t, cur)
			second, end, err := NormalizeIncremental(g.Provider, raw[cur.Offset:], cur)
			if err != nil {
				t.Fatal(err)
			}
			var m merged
			m.add(first.Events)
			m.add(second.Events)
			got := end.Settle(m.events())
			if !reflect.DeepEqual(generic(t, got), generic(t, whole.Events)) || !reflect.DeepEqual(second.Reasons, whole.Reasons) {
				t.Fatalf("%s split at %d:\n events %s\n  want %s\nreasons %v\n   want %v", g.name, k,
					mustJSON(got), mustJSON(whole.Events), second.Reasons, whole.Reasons)
			}
			if second.Complete != whole.Complete {
				t.Fatalf("%s split at %d: complete %v, want %v", g.name, k, second.Complete, whole.Complete)
			}
		}
	}
}

func TestIncrementalReplayIsIdempotent(t *testing.T) {
	for _, g := range loadGoldens(t) {
		raw := g.jsonl(t)
		half := len(raw) / 2
		_, cur, _ := NormalizeIncremental(g.Provider, raw[:half], Cursor{})
		cur = persisted(t, cur)
		a, endA, _ := NormalizeIncremental(g.Provider, raw[cur.Offset:], cur)
		b, endB, _ := NormalizeIncremental(g.Provider, raw[cur.Offset:], cur)
		if !reflect.DeepEqual(generic(t, a), generic(t, b)) || !reflect.DeepEqual(generic(t, endA), generic(t, endB)) {
			t.Fatalf("%s: replaying from a committed cursor changed the result", g.name)
		}
		// Nothing new: a further call consumes nothing and emits nothing.
		c, endC, _ := NormalizeIncremental(g.Provider, raw[endA.Offset:], endA)
		if len(c.Events) != 0 || endC.Offset != endA.Offset || endC.PrefixHash != endA.PrefixHash {
			t.Fatalf("%s: a call with nothing new emitted %d events or moved the cursor", g.name, len(c.Events))
		}
	}
}

func TestIncrementalHoldsAnUnterminatedLine(t *testing.T) {
	line := `{"type":"session_meta","payload":{"id":"s"}}`
	b, cur, err := NormalizeIncremental("codex", []byte(line), Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if cur.Offset != 0 || cur.Line != 0 || !contains(b.Reasons, "truncated_tail:0") {
		t.Fatalf("an unterminated line was consumed: offset %d, reasons %v", cur.Offset, b.Reasons)
	}
	_, cur, _ = NormalizeIncremental("codex", []byte(line+"\n"), cur)
	if cur.Offset != int64(len(line)+1) || cur.Line != 1 {
		t.Fatalf("the completed line was not consumed: %+v", cur)
	}
}

func TestIncrementalRefusesAnotherProvidersCursor(t *testing.T) {
	_, cur, _ := NormalizeIncremental("claude", nil, Cursor{})
	if _, _, err := NormalizeIncremental("codex", nil, cur); err == nil || !strings.Contains(err.Error(), "cannot continue") {
		t.Fatalf("err = %v", err)
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
