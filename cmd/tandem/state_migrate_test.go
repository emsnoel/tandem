package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// startedRun is a repository with one started run that nothing has ticked.
func startedRun(t *testing.T) (root, home, run string) {
	t.Helper()
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	e := newCLI(t)
	rc, got, errOut := e.start()
	if rc != exitcode.OK {
		t.Fatalf("start: rc %d %s", rc, errOut)
	}
	return e.root, e.home, got["run_id"].(string)
}

func TestStateMigrationCommandsRejectMalformedUse(t *testing.T) {
	for _, args := range [][]string{
		{"state", "export"},
		{"state", "export", "--run", "x"},
		{"state", "export", "--output", "y"},
		{"state", "recover"},
		{"state", "rebind", "--checkout", "x"},
		{"state", "rebind", "--root", "/tmp"},
		{"state", "gc", "extra"},
		{"state", "gc", "--nonsense"},
		{"state", "export", "--run", "x", "--output", "y", "stray"},
	} {
		rc, out, errOut := runCapture(t, args...)
		if rc != exitcode.Usage || out != "" || errOut == "" {
			t.Errorf("%v: rc %d stdout %q stderr %q", args, rc, out, errOut)
		}
	}
}

func TestStateGCReportsByDefaultAndAppliesOnlyWhenAsked(t *testing.T) {
	root, home, _ := startedRun(t)
	// The run is running, so apply is refused and nothing is touched.
	before := snapshot(t, home)
	rc, out, errOut := runCapture(t, "state", "gc", "--apply", "--root", root, "--state-home", home)
	if rc != exitcode.ManualGate || out != "" || !strings.Contains(errOut, "idle") {
		t.Errorf("apply on a busy checkout: rc %d stdout %q stderr %q", rc, out, errOut)
	}
	// The report is one JSON object with the documented keys, and writes nothing.
	rc, out, errOut = runCapture(t, "state", "gc", "--root", root, "--state-home", home)
	if rc != exitcode.OK {
		t.Fatalf("report rc %d: %s", rc, errOut)
	}
	v := oneJSON(t, out)
	for _, k := range []string{"apply", "candidates", "deleted", "retained", "missing", "corrupt"} {
		if _, ok := v[k]; !ok {
			t.Errorf("report has no %q: %v", k, v)
		}
	}
	if v["apply"] != false || len(v["deleted"].([]any)) != 0 {
		t.Errorf("a report deleted: %v", v)
	}
	if !sameSnapshot(before, snapshot(t, home)) {
		t.Error("the report or the refused apply changed the state home")
	}
	// A checkout with no state is a refusal, not a crash.
	if rc, out, _ := runCapture(t, "state", "gc", "--root", t.TempDir(), "--state-home", home); rc == exitcode.OK || out != "" {
		t.Errorf("no state: rc %d %q", rc, out)
	}
}

func TestStateMigrationEndToEnd(t *testing.T) {
	root, home, run := startedRun(t)
	rc, out, errOut := runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
	if st := oneJSON(t, out); rc != exitcode.OK || st["engine"] != "durable" {
		t.Fatalf("status rc %d %v %s", rc, st, errOut)
	}

	// Export, then refuse to overwrite it.
	dest := filepath.Join(t.TempDir(), "export")
	rc, out, errOut = runCapture(t, "state", "export", "--run", run, "--output", dest, "--root", root, "--state-home", home)
	if rc != exitcode.OK {
		t.Fatalf("export rc %d: %s", rc, errOut)
	}
	ex := oneJSON(t, out)
	if ex["run_id"] != run || ex["artifacts"].(float64) < 5 {
		t.Errorf("%v", ex)
	}
	if _, err := os.Stat(filepath.Join(dest, "COMPLETE")); err != nil {
		t.Errorf("no completion marker: %v", err)
	}
	if rc, out, _ := runCapture(t, "state", "export", "--run", run, "--output", dest, "--root", root, "--state-home", home); rc != exitcode.ManualGate || out != "" {
		t.Errorf("an export over an existing directory: rc %d %q", rc, out)
	}

	// A run that is running cannot be rebound.
	rc, out, _ = runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
	id := oneJSON(t, out)["checkout_id"].(string)
	if rc, out, _ := runCapture(t, "state", "rebind", "--checkout", id, "--root", root, "--state-home", home); rc != exitcode.ManualGate || out != "" {
		t.Errorf("rebind of a running checkout: rc %d %q", rc, out)
	}
	// An unknown run is a refusal, not a crash.
	if rc, out, _ := runCapture(t, "state", "recover", "--run", strings.Repeat("0", 32), "--root", root, "--state-home", home); rc != exitcode.ManualGate || out != "" {
		t.Errorf("recover of an unknown run: rc %d %q", rc, out)
	}
}

func TestStateArchiveEndsARunWithoutLosingIt(t *testing.T) {
	root, home, run := startedRun(t)
	var rc int
	var out, errOut string
	// A running run is not archived.
	if rc, out, _ := runCapture(t, "state", "archive", "--run", run, "--root", root, "--state-home", home); rc != exitcode.ManualGate || out != "" {
		t.Fatalf("archive of a running run: rc %d %q", rc, out)
	}
	if rc, _, errOut := runCapture(t, "build", "stop", "--root", root, "--state-home", home); rc != exitcode.OK {
		t.Fatalf("stop: %d %s", rc, errOut)
	}
	// The first task's prompt was never sent, so the next tick cancels
	// it and the run settles without any agent being asked.
	rc, out, errOut = runCapture(t, "build", "tick", "--root", root, "--state-home", home)
	if v := oneJSON(t, out); rc != exitcode.OK || v["state"] != "paused" {
		t.Fatalf("tick: rc %d %v %s", rc, v, errOut)
	}
	// A marker an earlier archive set aside, under the name older tandems used.
	relay := paths.Relay(root)
	earlier := []byte(`{"engine_version":2,"protocol_version":2,"run_id":"` + strings.Repeat("e", 32) + `"}` + "\n")
	if err := os.WriteFile(filepath.Join(relay, "active-run.json.archived"), earlier, 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out, errOut = runCapture(t, "state", "archive", "--run", run, "--root", root, "--state-home", home)
	if rc != exitcode.OK {
		t.Fatalf("archive rc %d: %s", rc, errOut)
	}
	if v := oneJSON(t, out); v["state"] != "archived" || v["marker"] != "archived" {
		t.Errorf("%v", v)
	}
	if _, err := os.Stat(filepath.Join(relay, "active-run.json")); !os.IsNotExist(err) {
		t.Error("the marker still names an archived run")
	}
	if _, err := os.Stat(filepath.Join(relay, "active-run."+run+".archived.json")); err != nil {
		t.Errorf("the marker was not set aside under its run's name: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(relay, "active-run.json.archived")); string(got) != string(earlier) {
		t.Errorf("archiving overwrote the marker an earlier archive set aside: %q", got)
	}
	// The history is still there to read.
	rc, out, _ = runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
	if rc != exitcode.OK {
		t.Fatalf("status rc %d", rc)
	}
	dest := filepath.Join(t.TempDir(), "export")
	if rc, _, errOut := runCapture(t, "state", "export", "--run", run, "--output", dest, "--root", root, "--state-home", home); rc != exitcode.OK {
		t.Errorf("an archived run cannot be exported: %d %s", rc, errOut)
	}
	// Archiving again is harmless.
	if rc, _, _ := runCapture(t, "state", "archive", "--run", run, "--root", root, "--state-home", home); rc != exitcode.OK {
		t.Errorf("a second archive: rc %d", rc)
	}
}
