package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/paths"
)

func TestBuildArchiveFailureIsAnError(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	b := load(t, dir)
	b.Tasks[0].Report = filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(b.Tasks[0].Report, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	devnull, _ := os.Open(os.DevNull)
	old := os.Stderr
	os.Stderr = devnull
	defer func() { os.Stderr = old }()
	if rc := cmdBuild([]string{"archive", "--root", root}); rc == 0 {
		t.Fatal("an escaping report was archived")
	}
}

// A new plan replaces the old build; its evidence must survive in history.
func TestStartingAnotherPlanArchivesTheBuildItReplaces(t *testing.T) {
	root, dir := pausedOrDoneBuild(t)
	other := filepath.Join(root, "other.md")
	if err := os.WriteFile(other, []byte(strings.Replace(threeTaskPlan, "Punctuate", "Different", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, rc := runStartPlan(t, root, other); rc != 0 {
		t.Fatalf("start rc %d", rc)
	}
	archives, _ := filepath.Glob(filepath.Join(root, build.HistoryDir, "legacy", "b1-*"))
	if len(archives) != 1 {
		t.Fatalf("archives %v", archives)
	}
	if got := load(t, dir); got.Tasks[0].Title != "Different" {
		t.Fatalf("the new plan did not start: %+v", got.Tasks[0])
	}
	archived, _ := build.Load(filepath.Join(archives[0], "build"))
	if raw, err := os.ReadFile(archived.Tasks[0].Report); err != nil || string(raw) != "the old report" {
		t.Fatalf("archived report %q: %v", raw, err)
	}
}

func pausedOrDoneBuild(t *testing.T) (root, dir string) {
	t.Helper()
	root, dir = buildRepo(t, "", 0)
	b := load(t, dir)
	b.Tasks[0].Report = filepath.Join(paths.Relay(root), "old-report.md")
	if err := os.WriteFile(b.Tasks[0].Report, []byte("the old report"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.Tasks[0].Status = build.StatusDone
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	return root, dir
}

func runStartPlan(t *testing.T, root, planPath string) (string, int) {
	t.Helper()
	return captureStdoutRC(t, func() int { return cmdBuildStart([]string{"--plan", planPath, "--root", root}) })
}
