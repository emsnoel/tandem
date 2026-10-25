package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestFindCodexLocatesARolloutBySessionID(t *testing.T) {
	dir := t.TempDir()
	day := filepath.Join(dir, "2026", "09", "28")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "01a0e90d-a3fe-7c01-8ac5-3b1f03a9828a"
	want := filepath.Join(day, "rollout-2026-09-28T18-26-20-"+id+".jsonl")
	for _, f := range []string{want, filepath.Join(day, "rollout-2026-09-28T18-26-20-01a0e90d-0000-0000-0000-000000000000.jsonl")} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := findCodexRollout(dir, id)
	if err != nil || got != want {
		t.Fatalf("FindCodex = %q, %v; want %q", got, err, want)
	}
	if got, err := findCodexRollout(dir, "no-such-session"); err != nil || got != "" {
		t.Errorf("unknown id: %q, %v", got, err)
	}
	if got, err := findCodexRollout(dir, ""); err != nil || got != "" {
		t.Errorf("empty id: %q, %v", got, err)
	}
}

func TestFindCodexChildrenFollowsSubagentsAtAnyDepth(t *testing.T) {
	dir := t.TempDir()
	day := filepath.Join(dir, "2026", "09", "28")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := func(id, parent string) string {
		p := ""
		if parent != "" {
			p = `,"parent_thread_id":"` + parent + `"`
		}
		return `{"type":"session_meta","payload":{"id":"` + id + `"` + p + `,"base_instructions":{"text":"` + strings.Repeat("x", 100000) + `"}}}` + "\n"
	}
	write := func(id, parent string) string {
		f := filepath.Join(day, "rollout-2026-09-28T23-00-00-"+id+".jsonl")
		if err := os.WriteFile(f, []byte(meta(id, parent)), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	parent := write("p", "")
	child := write("c1", "p")
	grandchild := write("g1", "c1")
	write("other", "someone-else")
	got := findCodexChildren(dir, parent, "p")
	sort.Strings(got)
	want := []string{child, grandchild}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("children = %v, want %v", got, want)
	}
}
