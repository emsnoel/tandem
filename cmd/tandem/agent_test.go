package main

import (
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/herdr"
)

func s(v string) *string { return &v }

func TestSnapshotLineIsThreeTabFields(t *testing.T) {
	a := &herdr.Agent{PaneID: "p1", CWD: "/repo", Name: s("builder"), Status: s("idle")}
	got, err := snapshotLine(a)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "p1\t/repo\tidle" {
		t.Fatalf("want %q, got %q", "p1\t/repo\tidle", got)
	}
	if len(strings.Split(got, "\t")) != 3 {
		t.Fatalf("want 3 tab-separated fields, got %d", len(strings.Split(got, "\t")))
	}
}

// A path with a space must survive: bash reads this with IFS=$'\t', so only
// tabs are separators.
func TestSnapshotLinePreservesSpacesInCWD(t *testing.T) {
	a := &herdr.Agent{PaneID: "p1", CWD: "/path with spaces/repo", Name: s("b"), Status: s("idle")}
	got, err := snapshotLine(a)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Split(got, "\t")[1] != "/path with spaces/repo" {
		t.Fatalf("spaces in cwd were mangled: %q", got)
	}
}

func TestSnapshotLineRendersAbsentStatusAsUnknown(t *testing.T) {
	a := &herdr.Agent{PaneID: "p1", CWD: "/repo", Name: s("builder"), Status: nil}
	got, _ := snapshotLine(a)
	if got != "p1\t/repo\tunknown" {
		t.Fatalf("want trailing unknown, got %q", got)
	}
}

func TestSnapshotLineRejectsEmbeddedTabs(t *testing.T) {
	a := &herdr.Agent{PaneID: "p1", CWD: "/has\ttab", Name: s("b"), Status: s("idle")}
	if _, err := snapshotLine(a); err == nil {
		t.Fatal("want an error for a tab in cwd, got nil")
	}
}

func TestPromptArgsUsesHerdrFlagNames(t *testing.T) {
	got := promptArgs("builder", "do the thing", true, nil, 120000)
	want := []string{"agent", "prompt", "builder", "do the thing", "--wait", "--timeout", "120000"}
	if len(got) != len(want) {
		t.Fatalf("want %d args, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestPromptArgsOmitsDisabledOptions(t *testing.T) {
	got := promptArgs("builder", "x", false, nil, 0)
	want := []string{"agent", "prompt", "builder", "x"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestReadArgsIncludesPositiveLineCount(t *testing.T) {
	got := readArgs("builder", "", 80)
	want := []string{"agent", "read", "builder", "--lines", "80"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestWaitArgsUsesHerdrTimeoutFlag(t *testing.T) {
	got := waitArgs("builder", nil, 120000)
	want := []string{"agent", "wait", "builder", "--timeout", "120000"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestPaneRejectsUnknownSubcommand(t *testing.T) {
	if got := cmdPane([]string{"explode"}); got != 2 {
		t.Fatalf("want usage exit 2, got %d", got)
	}
}

func TestWaitArgsRepeatsUntil(t *testing.T) {
	got := waitArgs("planner", []string{"idle", "done", "working"}, 90000)
	want := []string{"agent", "wait", "planner",
		"--until", "idle", "--until", "done", "--until", "working",
		"--timeout", "90000"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestReadArgsPassesSourceThrough(t *testing.T) {
	got := readArgs("builder", "recent-unwrapped", 120)
	want := []string{"agent", "read", "builder", "--source", "recent-unwrapped", "--lines", "120"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestPromptArgsRepeatsUntil(t *testing.T) {
	got := promptArgs("builder", "x", true, []string{"idle", "done"}, 1000)
	want := []string{"agent", "prompt", "builder", "x", "--wait", "--until", "idle", "--until", "done", "--timeout", "1000"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestRenameArgsForwardsOpaqueTail(t *testing.T) {
	got := renameArgs("w1:p2", []string{"builder"})
	want := []string{"agent", "rename", "w1:p2", "builder"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestAgentArgsForwardsList(t *testing.T) {
	got := agentArgs("list", nil)
	want := []string{"agent", "list"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestPaneArgsForwardMutationFlagsVerbatim(t *testing.T) {
	tests := [][]string{
		{"split", "w1:p1", "--direction", "right", "--no-focus"},
		{"report-metadata", "w1:p2", "--source", "tandem-pair", "--token", "role=BUILD"},
		{"rename", "w1:p2", "builder (copilot)"},
	}
	for _, args := range tests {
		got := paneArgs(args)
		want := append([]string{"pane"}, args...)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}
