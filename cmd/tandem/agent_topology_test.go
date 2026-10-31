package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// stubHerdr puts a herdr on PATH that prints out for `agent list` and fails
// every other call the way Herdr reports an unknown agent.
func stubHerdr(t *testing.T, list string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "list.json"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"agent list\" ]; then cat '" + filepath.Join(dir, "list.json") + "'; exit 0; fi\n" +
		"echo '{\"error\":{\"code\":\"agent_not_found\",\"message\":\"not found\"}}' >&2; exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const twoClaudes = `{"result":{"agents":[
 {"pane_id":"w1:p1","agent":"claude","name":null,"agent_status":"idle","cwd":"/a\nb","tab_id":"w1:t1"},
 {"pane_id":"w1:p2","agent":"claude","name":"bob","agent_status":"idle","cwd":"/c","tab_id":"w1:t1"},
 {"pane_id":"w1:p3","agent":"codex","name":null,"agent_status":"idle","cwd":"/d","tab_id":"w1:t1"}]}}`

func fields(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\x00"), "\x00")
}

// Review focus 4: a cwd holding a newline comes back whole.
func TestAgentPlannerPrintsNULFields(t *testing.T) {
	stubHerdr(t, twoClaudes)
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"planner"}) })
	got := fields(out)
	if rc != exitcode.OK || len(got) != 4 || got[0] != "w1:p1" || got[1] != "/a\nb" || got[2] != "w1:t1" || got[3] != "" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
}

func TestAgentPlannerExitCodes(t *testing.T) {
	stubHerdr(t, twoClaudes)
	t.Setenv("HERDR_PANE_ID", "w1:p3")
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"planner"}) })
	if got := fields(out); rc != exitcode.ManyPlanners || len(got) != 6 || got[0] != "w1:p1" || got[1] != "" || got[4] != "bob" || got[5] != "/c" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
	out, rc = captureStdoutRC(t, func() int { return cmdAgent([]string{"planner", "--kind", "opencode"}) })
	if rc != exitcode.NoPlanner || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestAgentAdoptableEmptyWhenNone(t *testing.T) {
	stubHerdr(t, twoClaudes)
	out, rc := captureStdoutRC(t, func() int {
		return cmdAgent([]string{"adoptable", "--exclude-pane", "w1:p2", "--tab", "w1:t1"})
	})
	if rc != exitcode.OK || out != "" {
		t.Fatalf("two unnamed agents: rc %d, out %q", rc, out)
	}
	out, rc = captureStdoutRC(t, func() int {
		return cmdAgent([]string{"adoptable", "--exclude-pane", "w1:p1", "--tab", "w1:t1"})
	})
	if got := fields(out); rc != exitcode.OK || len(got) != 2 || got[0] != "w1:p3" || got[1] != "codex" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
}

func TestAgentKindNotFound(t *testing.T) {
	stubHerdr(t, twoClaudes)
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"kind", "bob"}) })
	if rc != exitcode.OK || out != "claude\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
	if _, rc = captureStdoutRC(t, func() int { return cmdAgent([]string{"kind", "nobody"}) }); rc != exitcode.HerdrError {
		t.Fatalf("rc %d, want %d", rc, exitcode.HerdrError)
	}
}
