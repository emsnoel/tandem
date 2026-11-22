package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// stubHerdr puts a herdr on PATH that prints list for `agent list`.
func stubHerdr(t *testing.T, list string) {
	t.Helper()
	stubHerdrAnswers(t, map[string]string{"agent list": list})
}

// stubHerdrAnswers puts a herdr on PATH that prints answers["$1 $2"] and
// fails every other call the way Herdr reports an unknown agent.
func stubHerdrAnswers(t *testing.T, answers map[string]string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n"
	i := 0
	for verb, out := range answers {
		f := filepath.Join(dir, fmt.Sprintf("answer%d", i))
		i++
		if err := os.WriteFile(f, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		script += "if [ \"$1 $2\" = '" + verb + "' ]; then cat '" + f + "'; exit 0; fi\n"
	}
	script += "echo '{\"error\":{\"code\":\"agent_not_found\",\"message\":\"not found\"}}' >&2; exit 1\n"
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
	if rc != exitcode.OK || len(got) != 5 || got[0] != "w1:p1" || got[1] != "/a\nb" || got[2] != "w1:t1" || got[3] != "" || got[4] != "claude" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
}

func TestAgentPlannerExitCodes(t *testing.T) {
	stubHerdr(t, twoClaudes)
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	t.Setenv("HERDR_PANE_ID", "")
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"planner"}) })
	// The unnamed Codex can plan too, so it is the third candidate.
	if got := fields(out); rc != exitcode.ManyPlanners || len(got) != 9 || got[0] != "w1:p1" || got[1] != "" || got[4] != "bob" || got[5] != "/c" || got[6] != "w1:p3" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
	out, rc = captureStdoutRC(t, func() int { return cmdAgent([]string{"planner", "--kind", "copilot"}) })
	if rc != exitcode.NoPlanner || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

const copilotPlanner = `{"result":{"agents":[
 {"pane_id":"w1:p1","agent":"copilot","name":null,"agent_status":"idle","cwd":"/a","tab_id":"w1:t1"},
 {"pane_id":"w1:p2","agent":"codex","name":null,"agent_status":"idle","cwd":"/b","tab_id":"w1:t1"}]}}`

func TestAgentPlannerAdoptsCopilot(t *testing.T) {
	stubHerdr(t, copilotPlanner)
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"planner"}) })
	if got := fields(out); rc != exitcode.OK || got[0] != "w1:p1" || got[4] != "copilot" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
}

// A Codex started plainly cannot reach Herdr, and tandem-pair from it says how
// to start one that can. Outside Codex the same failure gets no Codex advice.
func TestAgentPlannerFromAPlainCodexNamesTandemCodex(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'Error: Os { code: 1, kind: PermissionDenied, message: \"Operation not permitted\" }' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	for _, sandbox := range []string{"seatbelt", ""} {
		t.Setenv("CODEX_SANDBOX", sandbox)
		var rc int
		errOut := captureStderr(t, func() { _, rc = captureStdoutRC(t, func() int { return cmdAgent([]string{"planner"}) }) })
		if rc == exitcode.OK {
			t.Fatalf("CODEX_SANDBOX=%q: rc 0", sandbox)
		}
		if got := strings.Contains(errOut, "tandem codex"); got != (sandbox != "") {
			t.Errorf("CODEX_SANDBOX=%q: hint shown %v: %q", sandbox, got, errOut)
		}
	}
}

// opencodeBeside is a Copilot planner beside an agent Herdr detects that tandem
// cannot run.
const opencodeBeside = `{"result":{"agents":[
 {"pane_id":"w1:p1","agent":"copilot","name":null,"agent_status":"idle","cwd":"/a","tab_id":"w1:t1"},
 {"pane_id":"w1:p2","agent":"opencode","name":null,"agent_status":"idle","cwd":"/b","tab_id":"w1:t1"}]}}`

// Every kind tandem runs can plan. An agent of any other kind cannot, however
// it is chosen.
func TestOtherAgentsCannotPlan(t *testing.T) {
	stubHerdr(t, opencodeBeside)
	cfg := filepath.Join(t.TempDir(), "tandem.json")
	t.Setenv("HERDR_TANDEM_CONFIG", cfg)
	t.Setenv("HERDR_PANE_ID", "")
	cases := map[string]func() []string{
		"an opencode pane named": func() []string { return []string{"planner", "--pane", "w1:p2"} },
		"run from an opencode pane": func() []string {
			t.Setenv("HERDR_PANE_ID", "w1:p2")
			return []string{"planner"}
		},
		"--kind opencode": func() []string { return []string{"planner", "--kind", "opencode"} },
		"planner_kind opencode": func() []string {
			os.WriteFile(cfg, []byte(`{"defaults":{"planner_kind":"opencode"}}`), 0o600)
			return []string{"planner"}
		},
	}
	for name, args := range cases {
		a := args()
		var rc int
		errOut := captureStderr(t, func() { _, rc = captureStdoutRC(t, func() int { return cmdAgent(a) }) })
		if rc != exitcode.NotAPlanner || !strings.Contains(errOut, "opencode") || !strings.Contains(errOut, "can't plan") {
			t.Errorf("%s: rc %d %q", name, rc, errOut)
		}
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

const trustAgents = `{"result":{"agents":[
 {"pane_id":"w1:p1","agent":"claude","name":"planner","agent_status":"idle","cwd":"/r"},
 {"pane_id":"w1:p2","agent":"codex","name":"builder","agent_status":"idle","cwd":"/r"},
 {"pane_id":"w1:p3","agent":"codex","name":null,"agent_status":"idle","cwd":"/r/sub"},
 {"pane_id":"w1:p4","agent":"codex","name":null,"agent_status":"idle","cwd":"/r-other"}]}}`

func TestAgentCountPrintsNumber(t *testing.T) {
	stubHerdr(t, trustAgents)
	out, rc := captureStdoutRC(t, func() int {
		return cmdAgent([]string{"count", "--kind", "codex", "--under", "/r", "--exclude-pane", "w1:p2"})
	})
	if rc != exitcode.OK || out != "1\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

// Review focus 2: a Herdr failure prints nothing, so the caller cannot read it
// as zero other agents.
func TestAgentCountHerdrFails(t *testing.T) {
	stubHerdrAnswers(t, map[string]string{})
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"count", "--kind", "codex", "--under", "/r"}) })
	if rc == exitcode.OK || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestAgentKindByPane(t *testing.T) {
	stubHerdr(t, trustAgents)
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"kind", "--pane", "w1:p1"}) })
	if rc != exitcode.OK || out != "claude\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestAgentKindByPaneNotFound(t *testing.T) {
	stubHerdr(t, trustAgents)
	out, rc := captureStdoutRC(t, func() int { return cmdAgent([]string{"kind", "--pane", "w9:p9"}) })
	if rc != exitcode.HerdrError || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}
