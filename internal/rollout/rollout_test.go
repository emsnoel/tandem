package rollout

import (
	"os"
	"strings"
	"testing"
)

// Expected values were computed from a real project's full rollout (three
// reviews, then the build).
func TestParseRealSession(t *testing.T) {
	f, err := os.Open("testdata/rollout-2026-09-25.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	ps, err := Parse(f, "rollout-2026-09-25.jsonl")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Phase{
		{Index: 1, Calls: 8, FirstContext: 26413, Peak: 55676, Input: 363331, Cached: 312832, Output: 3942, Polls: 1},
		{Index: 2, Calls: 14, FirstContext: 56329, Peak: 96655, Input: 1172266, Cached: 1128704, Output: 4489, Polls: 3},
		{Index: 3, Calls: 9, FirstContext: 97269, Peak: 139739, Input: 1111993, Cached: 1067392, Output: 2038, Polls: 3},
		{Index: 4, Calls: 46, FirstContext: 140729, Peak: 184683, Input: 7842100, Cached: 7788800, Output: 12396, Polls: 14},
	}
	if len(ps) != len(want) {
		t.Fatalf("got %d phases, want %d", len(ps), len(want))
	}
	for i, w := range want {
		g := ps[i]
		if g.Index != w.Index || g.Calls != w.Calls || g.FirstContext != w.FirstContext ||
			g.Peak != w.Peak || g.Input != w.Input || g.Cached != w.Cached ||
			g.Output != w.Output || g.Polls != w.Polls {
			t.Errorf("phase %d:\n got  %+v\n want %+v", w.Index, g, w)
		}
	}
	if ps[3].Yields["1000"] == 0 {
		t.Errorf("build phase should record the 1000ms yields that caused the polling, got %v", ps[3].Yields)
	}
	tot := Total(ps)
	if tot.Calls != 77 || tot.Input != 10489690 || tot.Polls != 21 {
		t.Errorf("total: got calls=%d input=%d polls=%d, want 77 10489690 21", tot.Calls, tot.Input, tot.Polls)
	}
}

// Without code mode, codex calls write_stdin as a plain function call.
func TestParseCountsDirectWriteStdin(t *testing.T) {
	in := `{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"response_item","payload":{"type":"function_call","name":"write_stdin","arguments":"{\"session_id\":1,\"yield_time_ms\":5000}"}}
{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":1}}}}`
	ps, err := Parse(strings.NewReader(in), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Polls != 1 || ps[0].Yields["5000"] != 1 {
		t.Fatalf("got %+v", ps)
	}
}

// A log with activity before any task_started still reports it, as phase 0.
func TestParseWithoutTaskStarted(t *testing.T) {
	in := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":7,"cached_input_tokens":0,"output_tokens":2}}}}`
	ps, err := Parse(strings.NewReader(in), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Index != 0 || ps[0].FirstContext != 7 {
		t.Fatalf("got %+v", ps)
	}
}

// token_count with info:null is emitted at session start; it is not a call.
func TestParseSkipsNullInfo(t *testing.T) {
	in := `{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"event_msg","payload":{"type":"token_count","info":null}}`
	ps, err := Parse(strings.NewReader(in), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Calls != 0 {
		t.Fatalf("got %+v", ps)
	}
}

// A session still being written can end mid-line. Report what is there.
func TestParseToleratesTruncatedFinalLine(t *testing.T) {
	in := `{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3,"cached_input_tokens":0,"output_tokens":1}}}}
{"type":"event_msg","payload":{"type":"tok`
	ps, err := Parse(strings.NewReader(in), "x")
	if err != nil {
		t.Fatalf("a truncated FINAL line must not fail: %v", err)
	}
	if len(ps) != 1 || ps[0].Calls != 1 {
		t.Fatalf("got %+v", ps)
	}
}

// Corruption anywhere else is a real error, not something to skip past.
func TestParseRejectsCorruptMiddleLine(t *testing.T) {
	in := "not json\n" + `{"type":"event_msg","payload":{"type":"task_started"}}`
	if _, err := Parse(strings.NewReader(in), "x"); err == nil {
		t.Fatal("want an error for a corrupt line that is not the last")
	}
}

// Two files -- the review session and the fresh build session -- are phases
// in order; Total spans both.
func TestTotalAcrossFiles(t *testing.T) {
	a := []Phase{{File: "a", Index: 1, Calls: 2, Input: 10, Polls: 1, FirstContext: 5, Peak: 6}}
	b := []Phase{{File: "b", Index: 1, Calls: 3, Input: 20, Polls: 0, FirstContext: 4, Peak: 9}}
	tot := Total(append(a, b...))
	if tot.Calls != 5 || tot.Input != 30 || tot.Polls != 1 || tot.Peak != 9 {
		t.Fatalf("got %+v", tot)
	}
}
