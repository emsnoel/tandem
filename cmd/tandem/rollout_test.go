package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// A fresh configuration keeps both conservative defaults: new builds run on the
// legacy engine, and triage never skips a review. Turning either on takes its
// own switch, and each has its own release decision.
func TestRolloutDefaultsRemainConservative(t *testing.T) {
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	t.Setenv("HERDR_TRIAGE_SKIP", "")

	// A relay with nothing in it selects the legacy engine, with the variable
	// unset or empty.
	relay := paths.Relay(t.TempDir())
	if eng, err := controller.SelectEngine(relay, os.Getenv("TANDEM_BUILD_ENGINE")); err != nil || eng != controller.EngineLegacy {
		t.Fatalf("engine %q %v: a fresh relay must start legacy", eng, err)
	}
	if eng, _ := controller.SelectEngine(relay, "legacy"); eng != controller.EngineLegacy {
		t.Errorf("engine %q for the explicit legacy value", eng)
	}
	// Only the explicit opt-in selects durable, and only at start.
	if eng, _ := controller.SelectEngine(relay, "durable"); eng != controller.EngineDurable {
		t.Errorf("engine %q for TANDEM_BUILD_ENGINE=durable", eng)
	}
	if _, err := controller.SelectEngine(relay, "newest"); err == nil {
		t.Error("an unknown engine name was accepted")
	}

	// Starting a build through the CLI with nothing set writes a legacy record
	// and no durable marker.
	root, _ := stoppedLegacy(t)
	os.RemoveAll(build.Dir(paths.Relay(root)))
	rc, out, errOut := runCapture(t, "build", "start", "--plan", filepath.Join(root, "p.md"), "--root", root, "--builder", "builder", "--planner", "planner", "--check", "true")
	if rc != exitcode.OK {
		t.Fatalf("start rc %d: %s", rc, errOut)
	}
	if v := oneJSON(t, out); v["task"] != float64(1) {
		t.Errorf("%v", v)
	}
	if _, err := os.Stat(filepath.Join(paths.Relay(root), "active-run.json")); !os.IsNotExist(err) {
		t.Error("a durable marker was written without the opt-in")
	}
	if b, err := build.Load(build.Dir(paths.Relay(root))); err != nil || b == nil {
		t.Errorf("no legacy build record: %v", err)
	}

	// With the skip switch unset, a judgment that clears every threshold, on a
	// calibrated cohort, still sends the task to review.
	root2, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root2, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: confidentVerdict("r1")}
	if got := advance(t, f, root2, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q: skip is off by default, so the review must happen", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing {
		t.Errorf("task %+v", task)
	}
}

var placeholder = regexp.MustCompile(`^[A-Z]+$`)

// Every command the operations guide shows is accepted by the parser: the verb
// exists and its flags are known. The commands run against an empty directory
// and state home, so they refuse for want of state; a malformed shape would
// exit 2 instead.
func TestOperationsCommandsMatchUsage(t *testing.T) {
	raw, err := os.ReadFile("../../guide/durable-runner-operations.md")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "state-home")
	t.Setenv("TANDEM_STATE_HOME", home)
	// The samples that read a run are durable-run commands, so DIR carries a
	// durable marker; the state behind it does not exist, so they refuse.
	if err := os.MkdirAll(paths.Relay(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"engine_version":2,"protocol_version":2,"run_id":"` + strings.Repeat("a", 32) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(paths.Relay(dir), "active-run.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	subst := map[string]string{"DIR": dir, "PATH": filepath.Join(dir, "out"), "ID": strings.Repeat("a", 32), "N": "1"}
	seen := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "    tandem ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		for i, f := range fields {
			if placeholder.MatchString(f) {
				v, ok := subst[f]
				if !ok {
					t.Fatalf("%q: no value for the placeholder %s", line, f)
				}
				fields[i] = v
			}
		}
		seen++
		rc, _, errOut := runCapture(t, fields...)
		if rc == exitcode.Usage {
			t.Errorf("%q is not accepted by the parser: %s", strings.TrimSpace(line), errOut)
		}
	}
	if seen < 15 {
		t.Errorf("only %d documented commands were checked; the guide's samples were not found", seen)
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("checking the samples created the state home")
	}
}
