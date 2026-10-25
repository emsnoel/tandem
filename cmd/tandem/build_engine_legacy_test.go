package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
)

// legacyBuild leaves a legacy build of the env's plan in its relay, in the
// given state, with a task report as its evidence.
func (e *cliEnv) legacyBuild(st string) string {
	e.t.Helper()
	dir := build.Dir(paths.Relay(e.root))
	b, _, err := build.Start(dir, plan.Split(cliPlan), e.plan, "sha", build.Options{ID: "old-build", Builder: "builder", Planner: "planner"}, false)
	if err != nil {
		e.t.Fatal(err)
	}
	report := filepath.Join(dir, "report-1.md")
	if err := os.WriteFile(report, []byte("task 1 done\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	for i := range b.Tasks {
		b.Tasks[i].Status = build.StatusDone
	}
	b.Tasks[0].Report = report
	b.State = st
	if err := b.Save(dir); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func historyEntries(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, ".tandem-history", "legacy", "*"))
	return m
}

func TestADurableStartArchivesAFinishedLegacyBuildAndTakesTheRelay(t *testing.T) {
	e := newCLI(t)
	dir := e.legacyBuild(build.StateFinished)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")

	rc, got, errOut := e.start()
	if rc != exitcode.OK || got["engine"] != "durable" || len(got["run_id"].(string)) != 32 {
		t.Fatalf("a durable start after a finished legacy build: rc %d %v %s", rc, got, errOut)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the finished legacy build is still in the relay, so the relay would hold both engines")
	}
	hist := historyEntries(t, e.root)
	if len(hist) != 1 || got["archived_legacy"] != hist[0] {
		t.Fatalf("the legacy build must be archived first and the archive named: %v, output %v", hist, got)
	}
	if raw, err := os.ReadFile(filepath.Join(hist[0], "build", "build.json")); err != nil || !strings.Contains(string(raw), "old-build") {
		t.Fatalf("the archive does not hold the build: %v", err)
	}
	if ar, _ := controller.ReadActiveRun(paths.Relay(e.root)); ar == nil || ar.RunID != got["run_id"] {
		t.Fatalf("marker %+v", ar)
	}
	if rc, out, _ := e.run("state", "engine", "--root", e.root); rc != exitcode.OK || !strings.Contains(out, `"durable"`) {
		t.Fatalf("state engine after the start: rc %d %s", rc, out)
	}
}

func TestADryRunDurableStartLeavesAFinishedLegacyBuildInPlace(t *testing.T) {
	e := newCLI(t)
	dir := e.legacyBuild(build.StateFinished)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, got, errOut := e.start("--dry-run")
	if rc != exitcode.OK || got["engine"] != "durable" || got["dry_run"] != true {
		t.Fatalf("dry run: rc %d %v %s", rc, got, errOut)
	}
	if got["archives_legacy"] != dir {
		t.Errorf("a dry run must say the legacy build would be archived: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.json")); err != nil {
		t.Fatal("a dry run moved the legacy build")
	}
	if h := historyEntries(t, e.root); len(h) != 0 {
		t.Fatalf("a dry run archived: %v", h)
	}
}

func TestADurableStartOverAnUnfinishedLegacyBuildSaysWhy(t *testing.T) {
	for _, st := range []string{build.StateRunning, build.StateStopped} {
		t.Run(st, func(t *testing.T) {
			e := newCLI(t)
			dir := e.legacyBuild(st)
			before, _ := os.ReadFile(filepath.Join(dir, "build.json"))
			t.Setenv("TANDEM_BUILD_ENGINE", "durable")
			rc, _, errOut := e.start()
			if rc == exitcode.OK {
				t.Fatal("a durable start replaced an unfinished legacy build")
			}
			for _, want := range []string{"unfinished legacy build", dir, "import-legacy --active"} {
				if !strings.Contains(errOut, want) {
					t.Errorf("the refusal does not say %q: %s", want, errOut)
				}
			}
			if strings.Contains(errOut, "flag provided but not defined") {
				t.Errorf("the refusal is a flag parse error: %s", errOut)
			}
			if after, _ := os.ReadFile(filepath.Join(dir, "build.json")); string(after) != string(before) {
				t.Error("the legacy build was changed")
			}
			if h := historyEntries(t, e.root); len(h) != 0 {
				t.Fatalf("an unfinished build was archived: %v", h)
			}
		})
	}
}
