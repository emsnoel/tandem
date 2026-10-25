package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// codexLog writes a rollout log for a session under dir/YYYY/MM/DD, filed on
// today's date as Codex files it. Child sessions are searched for only around
// the run's own days, so a fixed date stops finding them once it is in the past.
func codexLog(t *testing.T, dir, id, parent string, lines ...string) {
	t.Helper()
	now := time.Now()
	day := filepath.Join(dir, now.Format("2006/01/02"))
	os.MkdirAll(day, 0o755)
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"parent_thread_id":%q}}`, id, parent)
	body := meta + "\n" + strings.Join(lines, "\n") + "\n"
	// A session has one log, rewritten in place as Codex appends to it. A new file
	// per call could leave a stale one for the reader to find.
	path := filepath.Join(day, "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if old, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "rollout-*-"+id+".jsonl")); len(old) > 0 {
		path = old[0]
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func totalEv(in, cached, out int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`, in, cached, out)
}

func TestBuildUsageReportsATotalAndSaysWhatIsMissing(t *testing.T) {
	e, _ := startedCLI(t) // the fake transport reports session "cli" for the builder
	sessions := t.TempDir()
	read := func(extra ...string) (int, map[string]any, string) {
		rc, got, _, errOut := e.run3(append([]string{"build", "usage", "--root", e.root, "--state-home", e.home, "--sessions-dir", sessions, "--json"}, extra...)...)
		return rc, got, errOut
	}

	// No log yet for the session: unknown, never zero.
	rc, got, errOut := read()
	if rc != exitcode.OK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	total := got["total"].(map[string]any)
	if total["completeness"] != "unavailable" || total["input"] != nil || total["output"] != nil {
		t.Errorf("no log was found: %v", total)
	}

	// The log appears, with a child that cannot be read.
	codexLog(t, sessions, "cli", "", totalEv(100, 40, 5), totalEv(150, 60, 8))
	codexLog(t, sessions, "kid", "cli", `{not json`)
	rc, got, _ = read("--records")
	total = got["total"].(map[string]any)
	if rc != exitcode.OK || total["completeness"] != "partial" || total["input"] != float64(150) || total["cached_input"] != float64(60) || total["output"] != float64(8) {
		t.Fatalf("%v", total)
	}
	by := got["by_role"].(map[string]any)["builder"].(map[string]any)
	if by["input"] != float64(150) || by["completeness"] != "partial" {
		t.Errorf("%v", by)
	}
	if got["scope"] != "run_jobs" {
		t.Errorf("scope %v: a run report must say it is narrower than a collaboration's totals", got["scope"])
	}
	recs := got["records"].([]any)
	first := recs[0].(map[string]any)
	if len(recs) < 2 || first["provider"] != "codex" || first["config_hash"] == "" || len(first["source_id"].(string)) != 32 {
		t.Errorf("%v", recs)
	}
	if !strings.Contains(fmt.Sprint(got["notes"]), "kid") {
		t.Errorf("the unreadable child must be named: %v", got["notes"])
	}

	// The child's log is repaired: complete, and the uncached part can be given.
	codexLog(t, sessions, "kid", "cli", totalEv(20, 0, 3))
	_, got, _ = read()
	total = got["total"].(map[string]any)
	if total["completeness"] != "complete" || total["input"] != float64(170) || total["output"] != float64(11) {
		t.Errorf("%v", total)
	}

	// Plain text names the gap and prints no derived figure it cannot back.
	rc, out, _ := e.run("build", "usage", "--root", e.root, "--state-home", e.home, "--sessions-dir", sessions)
	if rc != exitcode.OK || !strings.Contains(out, "input 170") || !strings.Contains(out, "uncached 110") || !strings.Contains(out, "complete") {
		t.Errorf("%s", out)
	}
	codexLog(t, sessions, "kid2", "cli", `{not json`)
	_, out, _ = e.run("build", "usage", "--root", e.root, "--state-home", e.home, "--sessions-dir", sessions)
	if !strings.Contains(out, "partial") || strings.Contains(out, "uncached") {
		t.Errorf("a partial total must not show an uncached figure:\n%s", out)
	}
}

func TestBuildUsageNeedsADurableRunAndKnownFlags(t *testing.T) {
	e := newCLI(t)
	if rc, _, errOut := e.run("build", "usage", "--root", e.root); rc == exitcode.OK || !strings.Contains(errOut, "durable") {
		t.Errorf("no durable run: rc %d %q", rc, errOut)
	}
	e2, _ := startedCLI(t)
	if rc, _, _ := e2.run("build", "usage", "--root", e2.root, "--state-home", e2.home, "--nope"); rc != exitcode.Usage {
		t.Errorf("rc %d", rc)
	}
}

// A Copilot builder's usage is read from Copilot's own logs.
func TestBuildUsageReadsACopilotBuilder(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if rc, _, errOut := e.start("--builder-kind", "copilot", "--share-context"); rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	e.sub("tick")
	home := t.TempDir() // the fake transport reports session "cli" for the builder
	dir := filepath.Join(home, "session-state", "cli")
	os.MkdirAll(dir, 0o755)
	rec := `{"type":"session.usage_record","data":{"usage":{"model":"claude-sonnet-5.5","inputTokens":%d,"outputTokens":%d,"cacheReadTokens":%d,"cacheWriteTokens":0,"accounting":{"usageId":"%s"}}}}`
	body := `{"type":"session.start","data":{"sessionId":"cli"}}` + "\n" + fmt.Sprintf(rec, 300, 7, 100, "msg_1") + "\n" + fmt.Sprintf(rec, 400, 9, 250, "msg_2") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, got, _, errOut := e.run3("build", "usage", "--root", e.root, "--state-home", e.home, "--copilot-home", home, "--json")
	if rc != exitcode.OK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	total := got["total"].(map[string]any)
	if total["completeness"] != "complete" || total["input"] != float64(700) || total["cached_input"] != float64(350) || total["output"] != float64(16) {
		t.Errorf("a Copilot builder's usage: %v (notes %v)", total, got["notes"])
	}
	if rc, _, errOut := e.run("build", "budget", "--root", e.root, "--state-home", e.home, "--copilot-home", home, "--scope", "run", "--limit", "0"); rc != exitcode.OK {
		t.Errorf("build budget does not take --copilot-home: %d %s", rc, errOut)
	}
}
