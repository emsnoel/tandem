package usageledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/rollout"
)

// Hand-calculated expectations that this package and the external token eval's
// observer must both reproduce. The eval keeps a copy of these files, never of
// the parser code.
const goldens = "testdata/usage"

type golden struct {
	name     string
	Provider string            `json:"provider"`
	Events   []json.RawMessage `json:"events"`
	Expected json.RawMessage   `json:"expected"`
	Cross    bool              `json:"cross_check"`
}

func loadGoldens(t *testing.T) []golden {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(goldens, "*.json"))
	if err != nil || len(paths) < 20 {
		t.Fatalf("goldens %v: %v", paths, err)
	}
	var out []golden
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		g := golden{name: filepath.Base(p)}
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

// jsonl renders a golden's records as the log bytes they stand for: a record
// as one canonical JSON line (the bytes its expected source_hash covers), a
// string item as the raw line it is.
func (g golden) jsonl(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, item := range g.Events {
		var line string
		if err := json.Unmarshal(item, &line); err == nil {
			buf.WriteString(line)
		} else {
			dec := json.NewDecoder(bytes.NewReader(item))
			dec.UseNumber()
			var v any
			if err := dec.Decode(&v); err != nil {
				t.Fatal(err)
			}
			buf.WriteString(canonical(v))
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func generic(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUnknownOutputStaysUnknown(t *testing.T) {
	var event Event
	if err := json.Unmarshal([]byte(`{"input_total":15,"output":null}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.Output != nil {
		t.Fatal("unknown output became zero")
	}
	if event.InputTotal == nil || *event.InputTotal != 15 {
		t.Fatalf("input_total %v", event.InputTotal)
	}
}

func TestGoldensMatchThePythonObserver(t *testing.T) {
	for _, g := range loadGoldens(t) {
		t.Run(g.name, func(t *testing.T) {
			batch, err := Normalize(g.Provider, g.jsonl(t))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(g.Expected, &want); err != nil {
				t.Fatal(err)
			}
			if got := generic(t, batch); !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.MarshalIndent(got, "", " ")
				wantJSON, _ := json.MarshalIndent(want, "", " ")
				t.Fatalf("normalized output differs\n got: %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}

func total(events []Event, pick func(Event) *int64) int64 {
	var sum int64
	for _, e := range events {
		if v := pick(e); v != nil {
			sum += *v
		}
	}
	return sum
}

func TestRequiredGoldenTotals(t *testing.T) {
	byName := map[string]golden{}
	for _, g := range loadGoldens(t) {
		byName[g.name] = g
	}
	g := byName["claude-cache-components.json"]
	b, _ := Normalize("claude", g.jsonl(t))
	if e := b.Events[0]; *e.InputTotal != 15 || *e.CacheWrite != 5 || *e.CacheRead != 7 {
		t.Fatalf("Claude 3+5+7: %+v", e)
	}
	g = byName["codex-cumulative-duplicate.json"]
	b, _ = Normalize("codex", g.jsonl(t))
	var got []int64
	for _, e := range b.Events {
		got = append(got, *e.InputTotal)
	}
	if !reflect.DeepEqual(got, []int64{100, 0, 50}) || !b.Complete {
		t.Fatalf("Codex 100,100,150 contributed %v (complete %v)", got, b.Complete)
	}
}

func TestCountersMustBeNonNegativeInt64(t *testing.T) {
	for _, bad := range []string{`1.0`, `1e3`, `-1`, `true`, `"3"`, `9223372036854775808`} {
		line := `{"type":"assistant","uuid":"u1","sessionId":"s","message":{"id":"m1","model":"x","stop_reason":"end_turn",` +
			`"usage":{"input_tokens":` + bad + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`
		b, err := Normalize("claude", []byte(line+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if b.Events[0].InputTotal != nil || !contains(b.Reasons, "invalid_counter:input_tokens:0") || b.Complete {
			t.Errorf("input_tokens %s: %+v %v", bad, b.Events[0], b.Reasons)
		}
	}
}

func TestClaudeInputOverflowIsUnknown(t *testing.T) {
	line := `{"type":"assistant","uuid":"u1","sessionId":"s","message":{"id":"m1","model":"x","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":9223372036854775807,"cache_creation_input_tokens":1,"cache_read_input_tokens":0,"output_tokens":1}}}`
	b, err := Normalize("claude", []byte(line+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Events[0].InputTotal != nil || !contains(b.Reasons, "counter_overflow:0") {
		t.Fatalf("an overflowing sum: %+v %v", b.Events[0], b.Reasons)
	}
}

func TestUnknownProviderIsAnError(t *testing.T) {
	if _, err := Normalize("gemini", nil); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptySourceIsIncompleteWithEmptyLists(t *testing.T) {
	b, err := Normalize("codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if b.Complete || !strings.Contains(string(raw), `"events":[]`) || !strings.Contains(string(raw), `"superseded":[]`) ||
		!contains(b.Reasons, "no_usage_events") {
		t.Fatalf("empty source: %s", raw)
	}
}

func TestBlankLinesAreSkippedButKeepCursors(t *testing.T) {
	raw := `{"type":"session_meta","payload":{"id":"s"}}` + "\n\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}}}` +
		"\n" + `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	b, err := Normalize("codex", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Events) != 1 || b.Events[0].SourceCursor != 2 || !b.Complete {
		t.Fatalf("%+v %v", b.Events, b.Reasons)
	}
}

// The run-scoped builder report (internal/rollout) stays as it is: on the
// same Codex log it reports the same input, cached and output totals, and it
// has no field for anything else, so collaboration totals are a superset.
func TestLegacyBuilderUsageIsCompatibleAndNarrower(t *testing.T) {
	checked := 0
	for _, g := range loadGoldens(t) {
		if g.Provider != "codex" || !g.Cross {
			continue
		}
		raw := g.jsonl(t)
		b, err := Normalize("codex", raw)
		if err != nil {
			t.Fatal(err)
		}
		stream := rollout.ParseUsage(bytes.NewReader(raw), g.name)
		var legacy rollout.Counters
		for _, d := range stream.Deltas {
			legacy.Input += d.Counters.Input
			legacy.Cached += d.Counters.Cached
			legacy.Output += d.Counters.Output
		}
		want := rollout.Counters{Input: total(b.Events, func(e Event) *int64 { return e.InputTotal }),
			Cached: total(b.Events, func(e Event) *int64 { return e.CacheRead }),
			Output: total(b.Events, func(e Event) *int64 { return e.Output })}
		if legacy != want {
			t.Errorf("%s: legacy %+v, collaboration %+v", g.name, legacy, want)
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("only %d Codex goldens compared", checked)
	}
	if n := reflect.TypeOf(rollout.Counters{}).NumField(); n != 3 {
		t.Fatalf("legacy counters gained fields (%d); review the narrower-scope claim", n)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// A Codex log without reasoning_output_tokens (or cache_write_input_tokens)
// leaves only that counter unknown. Input, cache and output stay usable.
func TestOptionalCodexCountersLeaveOnlyThemselvesUnknown(t *testing.T) {
	raw := `{"type":"session_meta","payload":{"id":"s"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":10}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"cached_input_tokens":90,"output_tokens":15,"reasoning_output_tokens":6}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	b, err := Normalize("codex", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Complete {
		t.Fatalf("reasons %v", b.Reasons)
	}
	first, second := b.Events[0], b.Events[1]
	if *first.InputTotal != 100 || first.ReasoningOutput != nil {
		t.Fatalf("first %+v", first)
	}
	// The earlier total had no reasoning counter, so the second one cannot be
	// differenced: unknown, not 6.
	if *second.InputTotal != 50 || second.ReasoningOutput != nil {
		t.Fatalf("second: input %d reasoning %v", *second.InputTotal, second.ReasoningOutput)
	}
	bad := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":-3}}}}` + "\n"
	b, _ = Normalize("codex", []byte(bad))
	if b.Complete || !contains(b.Reasons, "invalid_counter:reasoning_output_tokens:0") || b.Events[0].InputTotal != nil {
		t.Fatalf("an invalid reasoning counter: %v", b.Reasons)
	}
}
