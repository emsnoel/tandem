package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
)

// stoppedLegacy is a committed repository holding a stopped legacy build whose
// first task is done, with the plan it was built from.
func stoppedLegacy(t *testing.T) (root, home string) {
	t.Helper()
	root = t.TempDir()
	for _, args := range [][]string{{"init", "-q", root}, {"-C", root, "config", "user.email", "t@example.com"}, {"-C", root, "config", "user.name", "t"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".tandem-relay/\n.tandem-history/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(root, "p.md")
	if err := os.WriteFile(planPath, []byte(threeTaskPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", root, "add", "."}, {"-C", root, "commit", "-qm", "plan"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	root, _ = filepath.EvalSymlinks(root)
	planPath = filepath.Join(root, "p.md")
	sum := sha256.Sum256([]byte(threeTaskPlan))
	dir := build.Dir(paths.Relay(root))
	b, _, err := build.Start(dir, plan.Split(threeTaskPlan), planPath, hex.EncodeToString(sum[:]), build.Options{
		ID: "legacy-1", Builder: "builder", Planner: "planner", Check: "true", HistoryRoot: root, Now: "2026-10-01T10:00:00Z"}, false)
	if err != nil {
		t.Fatal(err)
	}
	b.Tasks[0].Status = build.StatusDone
	b.Tasks[1].Status = build.StatusSent
	b.State, b.Current = build.StateStopped, 2
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Relay(root), "builder.json"), []byte(`{"kind":"codex"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Relay(root), "reviewer.json"), []byte(`{"kind":"claude","name":"reviewer"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(t.TempDir(), "state-home")
}

func withProbe(t *testing.T, status string, err error) {
	t.Helper()
	old := herdrProbe
	herdrProbe = func(context.Context, string) (string, error) { return status, err }
	t.Cleanup(func() { herdrProbe = old })
}

func TestStateMigrationCommandsRejectMalformedUse(t *testing.T) {
	for _, args := range [][]string{
		{"state", "export"},
		{"state", "export", "--run", "x"},
		{"state", "export", "--output", "y"},
		{"state", "recover"},
		{"state", "rebind", "--checkout", "x"},
		{"state", "rebind", "--root", "/tmp"},
		{"state", "import-legacy"},
		{"state", "import-legacy", "--active", "/tmp"},
		{"state", "import-legacy", "--active", "/tmp", "--archive", "/tmp", "--quiescent"},
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
	root, home := stoppedLegacy(t)
	withProbe(t, "idle", nil)
	if rc, _, errOut := runCapture(t, "state", "import-legacy", "--active", root, "--quiescent", "--state-home", home); rc != exitcode.OK {
		t.Fatalf("setup: %d %s", rc, errOut)
	}
	// The imported run is running, so apply is refused and nothing is touched.
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

func TestStateImportActiveRefusesAWriterItCannotRuleOut(t *testing.T) {
	for name, tc := range map[string]struct {
		status string
		err    error
	}{
		"a working builder":    {"working", nil},
		"an unobservable pane": {"", errors.New("no such pane")},
	} {
		t.Run(name, func(t *testing.T) {
			root, home := stoppedLegacy(t)
			withProbe(t, tc.status, tc.err)
			rc, out, errOut := runCapture(t, "state", "import-legacy", "--active", root, "--quiescent", "--state-home", home)
			if rc != exitcode.ManualGate || out != "" || errOut == "" {
				t.Fatalf("rc %d stdout %q stderr %q", rc, out, errOut)
			}
			if _, err := os.Stat(filepath.Join(build.Dir(paths.Relay(root)), "build.json")); err != nil {
				t.Errorf("the legacy record was moved: %v", err)
			}
			if _, err := os.Stat(filepath.Join(paths.Relay(root), "active-run.json")); !os.IsNotExist(err) {
				t.Error("a durable run was started")
			}
		})
	}
	// Quiescence is asserted, never assumed.
	root, home := stoppedLegacy(t)
	withProbe(t, "idle", nil)
	if rc, out, _ := runCapture(t, "state", "import-legacy", "--active", root, "--state-home", home); rc != exitcode.Usage || out != "" {
		t.Errorf("without --quiescent: rc %d %q", rc, out)
	}
}

func TestStateMigrationEndToEnd(t *testing.T) {
	root, home := stoppedLegacy(t)
	withProbe(t, "idle", nil)
	rc, out, errOut := runCapture(t, "state", "import-legacy", "--active", root, "--quiescent", "--state-home", home)
	if rc != exitcode.OK {
		t.Fatalf("import rc %d: %s", rc, errOut)
	}
	v := oneJSON(t, out)
	run, _ := v["run_id"].(string)
	if len(run) != 32 || v["history_run_id"] == "" || v["resume"] != "task-2" {
		t.Fatalf("%v", v)
	}
	if carried, _ := v["carried"].([]any); len(carried) != 1 || carried[0] != "task-1" {
		t.Errorf("carried %v", v["carried"])
	}
	if un, _ := v["unresolved"].([]any); len(un) == 0 {
		t.Error("the report must say what stayed unresolved")
	}

	// The relay now belongs to the durable engine.
	rc, out, _ = runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
	if st := oneJSON(t, out); rc != exitcode.OK || st["engine"] != "durable" {
		t.Fatalf("status rc %d %v", rc, st)
	}
	// A second import has nothing to import.
	if rc, out, _ := runCapture(t, "state", "import-legacy", "--active", root, "--quiescent", "--state-home", home); rc != exitcode.ManualGate || out != "" {
		t.Errorf("a second import: rc %d %q", rc, out)
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

func TestStateEngineRefusesWhatItDoesNotSpeak(t *testing.T) {
	// The engine selector reads TANDEM_BUILD_ENGINE first. The cases here are
	// about what the relay holds, so an operator's exported choice must not
	// decide them.
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	for name, tc := range map[string]struct {
		marker string
		legacy bool
		want   int
		engine string
	}{
		"no relay at all":           {want: exitcode.OK, engine: "legacy"},
		"a legacy build":            {legacy: true, want: exitcode.OK, engine: "legacy"},
		"engine 99":                 {marker: `{"engine_version":99,"protocol_version":99,"run_id":"` + strings.Repeat("a", 32) + `"}`, want: exitcode.ManualGate},
		"protocol 3 on engine 2":    {marker: `{"engine_version":2,"protocol_version":3,"run_id":"` + strings.Repeat("a", 32) + `"}`, want: exitcode.ManualGate},
		"a marker that is not JSON": {marker: `{not json`, want: exitcode.ManualGate},
		"a marker beside a legacy build": {marker: `{"engine_version":2,"protocol_version":2,"run_id":"` + strings.Repeat("a", 32) + `"}`, legacy: true,
			want: exitcode.ManualGate},
		"a marker this tandem speaks": {marker: `{"engine_version":2,"protocol_version":2,"run_id":"` + strings.Repeat("a", 32) + `"}`, want: exitcode.OK, engine: "durable"},
	} {
		t.Run(name, func(t *testing.T) {
			root, _ := stoppedLegacy(t)
			relay := paths.Relay(root)
			if !tc.legacy {
				os.RemoveAll(filepath.Join(relay, "build"))
			}
			if tc.marker != "" {
				if err := os.WriteFile(filepath.Join(relay, "active-run.json"), []byte(tc.marker), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, relay)
			rc, out, errOut := runCapture(t, "state", "engine", "--root", root)
			if rc != tc.want {
				t.Fatalf("rc %d, want %d (stderr %q)", rc, tc.want, errOut)
			}
			if tc.want == exitcode.OK {
				if v := oneJSON(t, out); v["engine"] != tc.engine {
					t.Errorf("%v", v)
				}
			} else if out != "" || !strings.Contains(errOut, "install.sh") || !strings.Contains(errOut, "Nothing was changed") {
				t.Errorf("a refusal prints no result and the remedy: %q / %q", out, errOut)
			}
			if !sameSnapshot(before, snapshot(t, relay)) {
				t.Error("the check wrote to the relay")
			}
		})
	}
}

func TestStateArchiveEndsARunWithoutLosingIt(t *testing.T) {
	root, home := stoppedLegacy(t)
	withProbe(t, "idle", nil)
	rc, out, errOut := runCapture(t, "state", "import-legacy", "--active", root, "--quiescent", "--state-home", home)
	if rc != exitcode.OK {
		t.Fatalf("setup: %d %s", rc, errOut)
	}
	run := oneJSON(t, out)["run_id"].(string)
	// A running run is not archived.
	if rc, out, _ := runCapture(t, "state", "archive", "--run", run, "--root", root, "--state-home", home); rc != exitcode.ManualGate || out != "" {
		t.Fatalf("archive of a running run: rc %d %q", rc, out)
	}
	if rc, _, errOut := runCapture(t, "build", "stop", "--root", root, "--state-home", home); rc != exitcode.OK {
		t.Fatalf("stop: %d %s", rc, errOut)
	}
	// The prompt for the resumed task was never sent, so the next tick cancels
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
