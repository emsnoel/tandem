package rollout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.Local) }

// session writes a rollout log under sessions/YYYY/MM/DD, as Codex files them.
func session(t *testing.T, sessions string, when time.Time, id, parent string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(sessions, when.Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"parent_thread_id":%q}}`, id, parent)
	path := filepath.Join(dir, "rollout-"+when.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(meta+"\n"+strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// total is a token_count with cumulative counters.
func total(in, cached, out int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`, in, cached, out)
}

// last is a token_count carrying only the figures for one call.
func last(in, cached, out int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`, in, cached, out)
}

func ids(fs []Found) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.SessionID)
	}
	return strings.Join(out, ",")
}

func TestUsageFindsIntermediateDateDescendant(t *testing.T) {
	dir := t.TempDir()
	d1, d2, d3 := day(2026, 9, 25), day(2026, 9, 26), day(2026, 9, 27)
	session(t, dir, d1, "parent", "", total(10, 0, 1))
	session(t, dir, d2, "child", "parent", total(20, 0, 2))     // the day after the parent, a day before "today"
	session(t, dir, d3, "grandchild", "child", total(30, 0, 3)) // today
	session(t, dir, d2, "stranger", "someone-else", total(99, 0, 9))
	session(t, dir, day(2026, 9, 20), "too-early", "parent", total(1, 0, 1)) // outside the run's interval

	got, err := Descendants(dir, "parent", d1, d3)
	if err != nil {
		t.Fatal(err)
	}
	if ids(got) != "child,grandchild" {
		t.Fatalf("descendants %q: the child on the middle day must be found, once, and the stranger and the log from before the run must not", ids(got))
	}
	// Searching only the parent's day and today, as the old search did, misses it.
	if got, _ := Descendants(dir, "parent", d1, d1); ids(got) == "child,grandchild" {
		t.Error("the interval has no effect")
	}
	// Asking again gives the same answer in the same order.
	again, _ := Descendants(dir, "parent", d1, d3)
	if ids(again) != ids(got) && ids(again) != "child,grandchild" {
		t.Errorf("%q", ids(again))
	}
}

func TestUsageAncestryTerminatesOnCyclesAndFindsEachSessionOnce(t *testing.T) {
	dir := t.TempDir()
	d := day(2026, 9, 25)
	// root -> a -> b -> root again (a malformed log), and two logs for one session id.
	session(t, dir, d, "root", "b", total(1, 0, 1))
	session(t, dir, d, "a", "root", total(1, 0, 1))
	session(t, dir, d, "b", "a", total(1, 0, 1))
	other := d.Add(time.Hour)
	session(t, dir, other, "a", "root", total(5, 0, 1))
	got, err := Descendants(dir, "root", d, d)
	if err != nil || ids(got) != "a,b" {
		t.Fatalf("%q %v", ids(got), err)
	}
	if got, _ := Descendants(dir, "no-such-session", d, d); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestUsageCumulativeResetAndDuplicate(t *testing.T) {
	// 100, 150, 150 (the same event twice), then 20: the counter restarted.
	got := Deltas([]Counters{{Input: 100}, {Input: 150}, {Input: 150}, {Input: 20}})
	var steps []int64
	var sum int64
	for _, d := range got {
		steps = append(steps, d.Input)
		sum += d.Input
	}
	if fmt.Sprint(steps) != "[100 50 0 20]" || sum != 170 {
		t.Errorf("deltas %v sum %d, want [100 50 0 20] and 170", steps, sum)
	}
	// Any one counter going backwards starts a new segment for all three.
	got = Deltas([]Counters{{Input: 100, Cached: 40, Output: 10}, {Input: 120, Cached: 50, Output: 3}})
	if got[1] != (Counters{Input: 120, Cached: 50, Output: 3}) {
		t.Errorf("a reset in output alone: %+v", got[1])
	}
	if got := Deltas(nil); len(got) != 0 {
		t.Errorf("%v", got)
	}

	// The same through a log.
	dir := t.TempDir()
	path := session(t, dir, day(2026, 9, 25), "s", "", total(100, 10, 5), total(150, 20, 8), total(150, 20, 8), total(20, 0, 1))
	f, _ := os.Open(path)
	defer f.Close()
	s := ParseUsage(f, path)
	if s.Unavailable != "" || s.Truncated || len(s.Deltas) != 4 {
		t.Fatalf("%+v", s)
	}
	var in, cached, out int64
	for _, d := range s.Deltas {
		in, cached, out = in+d.Counters.Input, cached+d.Counters.Cached, out+d.Counters.Output
	}
	if in != 170 || cached != 20 || out != 9 {
		t.Errorf("input %d cached %d output %d", in, cached, out)
	}
	u, _ := Aggregate([]Stream{s})
	if *u.Input != 170 || *u.CachedInput != 20 || *u.Output != 9 || u.Completeness != workflow.UsageComplete || u.InputSemantics != workflow.SemanticsIncludesCache {
		t.Errorf("%+v", u)
	}
}

func TestUsagePerCallFiguresAreNotDifferenced(t *testing.T) {
	// A log with only per-call figures: each event is its own delta, and equal
	// consecutive calls are still two calls.
	dir := t.TempDir()
	path := session(t, dir, day(2026, 9, 25), "s", "", last(100, 90, 5), last(100, 90, 5), last(40, 0, 2))
	f, _ := os.Open(path)
	defer f.Close()
	s := ParseUsage(f, path)
	u, _ := Aggregate([]Stream{s})
	if len(s.Deltas) != 3 || *u.Input != 240 || *u.CachedInput != 180 || *u.Output != 12 {
		t.Errorf("%d deltas, %+v", len(s.Deltas), u)
	}
}

func TestUsageSourceIdsAreStableAndUnique(t *testing.T) {
	dir := t.TempDir()
	a := session(t, dir, day(2026, 9, 25), "one", "", total(10, 0, 1), total(20, 0, 2))
	b := session(t, dir, day(2026, 9, 25), "two", "", total(10, 0, 1), total(20, 0, 2))
	read := func(p string) Stream {
		f, _ := os.Open(p)
		defer f.Close()
		return ParseUsage(f, p)
	}
	s1, s1again, s2 := read(a), read(a), read(b)
	seen := map[string]bool{}
	for i, d := range s1.Deltas {
		if d.SourceID != s1again.Deltas[i].SourceID || len(d.SourceID) != 32 {
			t.Errorf("an id changed between reads: %q %q", d.SourceID, s1again.Deltas[i].SourceID)
		}
		for _, x := range append(s1.Deltas, s2.Deltas...) {
			_ = x
		}
		seen[d.SourceID] = true
	}
	for _, d := range s2.Deltas {
		if seen[d.SourceID] {
			t.Errorf("two sessions with identical content share source %s", d.SourceID)
		}
		seen[d.SourceID] = true
	}
	if len(seen) != 4 {
		t.Errorf("%d distinct ids for four events", len(seen))
	}
	// A log that is moved or copied elsewhere keeps its ids: they name the event,
	// not the directory it happens to be filed in.
	moved := filepath.Join(t.TempDir(), "elsewhere", filepath.Base(a))
	os.MkdirAll(filepath.Dir(moved), 0o755)
	raw, _ := os.ReadFile(a)
	os.WriteFile(moved, raw, 0o644)
	if m := read(moved); len(m.Deltas) != len(s1.Deltas) || m.Deltas[0].SourceID != s1.Deltas[0].SourceID || m.Deltas[1].SourceID != s1.Deltas[1].SourceID {
		t.Error("a copied log was given new source ids")
	}
	// A log that grows keeps the ids of what it already had.
	grown := session(t, dir, day(2026, 9, 25), "one", "", total(10, 0, 1), total(20, 0, 2), total(30, 0, 3))
	if g := read(grown); len(g.Deltas) != 3 || g.Deltas[0].SourceID != s1.Deltas[0].SourceID || g.Deltas[1].SourceID != s1.Deltas[1].SourceID {
		t.Errorf("a growing log renumbered its events")
	}
}

func TestUsageTruncatedTailAndUnreadableLogsStayPartial(t *testing.T) {
	dir := t.TempDir()
	d := day(2026, 9, 25)
	// A session still being written ends mid-line.
	trunc := session(t, dir, d, "live", "", total(100, 0, 5), total(150, 0, 8))
	f, _ := os.OpenFile(trunc, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_tok`)
	f.Close()
	r, _ := os.Open(trunc)
	s := ParseUsage(r, trunc)
	r.Close()
	if !s.Truncated || len(s.Deltas) != 2 || s.Unavailable != "" {
		t.Fatalf("%+v", s)
	}
	if u, _ := Aggregate([]Stream{s}); u.Completeness != workflow.UsagePartial || *u.Input != 150 {
		t.Errorf("a truncated tail is a lower bound, not a total: %+v", u)
	}

	// A log with garbage in the middle cannot be trusted at all.
	bad := session(t, dir, d, "garbled", "", total(10, 0, 1), `{not json`, total(20, 0, 2))
	r, _ = os.Open(bad)
	s = ParseUsage(r, bad)
	r.Close()
	if s.Unavailable == "" || len(s.Deltas) != 0 {
		t.Errorf("%+v", s)
	}
	// A file that cannot be opened.
	if s := ReadStream(filepath.Join(dir, "missing.jsonl")); s.Unavailable == "" {
		t.Errorf("%+v", s)
	}
}

func TestUsageMissingChildRemainsPartial(t *testing.T) {
	dir := t.TempDir()
	d := day(2026, 9, 25)
	session(t, dir, d, "parent", "", total(100, 20, 10))
	// An expected child whose log is unreadable beyond its first line.
	session(t, dir, d, "child", "parent", `{not json`, total(5, 0, 1))

	c := Collect(dir, "parent", d, d)
	if len(c.Streams) != 2 {
		t.Fatalf("%d streams", len(c.Streams))
	}
	u, notes := Aggregate(c.Streams)
	if u.Completeness != workflow.UsagePartial || u.Input == nil || *u.Input != 100 {
		t.Errorf("the known part is a lower bound, and the total is partial: %+v", u)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "child") {
		t.Errorf("the gap must be named: %v", notes)
	}
	if _, ok := u.Uncached(); ok {
		t.Error("an uncached figure from a partial total")
	}

	// Nothing readable at all: null, never zero.
	only := Collect(dir, "no-such-root", d, d)
	u, _ = Aggregate(only.Streams)
	if u.Completeness != workflow.UsageUnavailable || u.Input != nil || u.Output != nil {
		t.Errorf("%+v", u)
	}
	if len(only.Streams) != 1 || only.Streams[0].Unavailable == "" {
		t.Errorf("a root with no log is an unavailable source, not an empty result: %+v", only.Streams)
	}
}

func TestUsageNeverCountsAnAggregateAndItsChildren(t *testing.T) {
	dir := t.TempDir()
	d := day(2026, 9, 25)
	session(t, dir, d, "parent", "", total(300, 0, 30)) // reports 300 including what its child did
	session(t, dir, d, "child", "parent", total(100, 0, 10))
	c := Collect(dir, "parent", d, d)
	c.Streams[0].IncludesChildren = true
	u, notes := Aggregate(c.Streams)
	if *u.Input != 300 || *u.Output != 30 {
		t.Errorf("the child was counted twice: %+v", u)
	}
	if !strings.Contains(strings.Join(notes, " "), "included") {
		t.Errorf("%v", notes)
	}
	// Without the flag, a parent does not include its children and both count.
	c = Collect(dir, "parent", d, d)
	if u, _ := Aggregate(c.Streams); *u.Input != 400 {
		t.Errorf("%+v", u)
	}
}

func TestUsageRejectsCachedOverInput(t *testing.T) {
	dir := t.TempDir()
	path := session(t, dir, day(2026, 9, 25), "s", "", total(100, 10, 5), total(150, 200, 8), total(180, 30, 9))
	f, _ := os.Open(path)
	defer f.Close()
	s := ParseUsage(f, path)
	if s.Rejected != 1 || len(s.Diagnostics) == 0 || !strings.Contains(strings.Join(s.Diagnostics, " "), "cached") {
		t.Fatalf("the impossible record must be diagnosed: %+v", s)
	}
	u, _ := Aggregate([]Stream{s})
	if u.Completeness != workflow.UsagePartial {
		t.Errorf("a stream with a rejected record is partial: %+v", u)
	}
	// The valid records on either side are still counted, differenced across the gap.
	if *u.Input != 180 || *u.CachedInput != 30 {
		t.Errorf("%+v", u)
	}
}

func TestUsageModelIsReadFromTheLog(t *testing.T) {
	dir := t.TempDir()
	path := session(t, dir, day(2026, 9, 25), "s", "", `{"type":"turn_context","payload":{"model":"gpt-test-1"}}`, total(10, 0, 1))
	f, _ := os.Open(path)
	defer f.Close()
	if s := ParseUsage(f, path); len(s.Deltas) != 1 || s.Deltas[0].Model != "gpt-test-1" || s.Provider != "codex" || s.SessionID != "s" {
		t.Errorf("%+v", s)
	}
}

func TestLegacyPhasesConvertWithoutInventingFigures(t *testing.T) {
	u := FromPhase(Phase{Calls: 3, Input: 1000, Cached: 600, Output: 50})
	if *u.Input != 1000 || *u.CachedInput != 600 || *u.Output != 50 || u.Completeness != workflow.UsageComplete || u.InputSemantics != workflow.SemanticsIncludesCache {
		t.Errorf("%+v", u)
	}
	// A phase with no calls has no figures: unknown, not zero tokens used.
	if u := FromPhase(Phase{}); u.Completeness != workflow.UsageUnavailable || u.Input != nil {
		t.Errorf("%+v", u)
	}
}
