package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/herdr"
)

// runningProbe is okProbe with a builder already running in w1:p2.
type runningProbe struct {
	okProbe
	cwd string
}

func (r runningProbe) Agents(context.Context) ([]herdr.Agent, error) {
	name := "builder"
	return []herdr.Agent{{PaneID: "w1:p2", Name: &name, Kind: "claude", CWD: r.cwd}}, nil
}

// A re-run to repair a half-built pair: doctor finds the running builder.
func TestDoctorReportsARunningBuilder(t *testing.T) {
	root := healthyRepo(t)
	t.Setenv("HERDR_BUILDER_NAME", "")
	doctorProbe = runningProbe{cwd: root}
	out, _ := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude"}) })
	if !strings.Contains(out, "builder already runs in pane w1:p2") {
		t.Fatalf("doctor did not report the running builder:\n%s", out)
	}
}

// A builder with the pair's name that works elsewhere is not adopted, so doctor
// warns and does not skip the builder's checks.
func TestDoctorWarnsOfABuilderInAnotherCheckout(t *testing.T) {
	root := healthyRepo(t)
	t.Setenv("HERDR_BUILDER_NAME", "")
	other := t.TempDir()
	doctorProbe = runningProbe{cwd: other}
	out, _ := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude"}) })
	if strings.Contains(out, "builder already runs") {
		t.Fatalf("doctor counted a builder from another checkout:\n%s", out)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "pane w1:p2 holds 'builder' but works in "+other) {
		t.Fatalf("doctor did not warn:\n%s", out)
	}
}

func TestDoctorReportsNames(t *testing.T) {
	main := healthyRepo(t)
	for _, a := range [][]string{{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}} {
		if out, err := exec.Command("git", append([]string{"-C", main}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v\n%s", err, out)
		}
	}
	wt := filepath.Join(t.TempDir(), "wt-b")
	if out, err := exec.Command("git", "-C", main, "worktree", "add", "-q", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	for _, k := range []string{"HERDR_PLANNER_NAME", "HERDR_BUILDER_NAME", "HERDR_REVIEWER_NAME"} {
		t.Setenv(k, "")
	}
	doctorProbe = okProbe{}
	out, _ := captureStdoutRC(t, func() int {
		return cmdDoctor([]string{"--cwd", wt, "--state-home", t.TempDir(), "--builder", "claude", "--reviewer", "claude"})
	})
	if !strings.Contains(out, "this checkout pairs as planner-wt-b, builder-wt-b, reviewer-wt-b") {
		t.Fatalf("doctor did not report the names:\n%s", out)
	}
}

// Doctor writes nothing, even for a worktree whose names need a checkout id.
func TestDoctorLeavesAFreshStateHomeEmpty(t *testing.T) {
	main := filepath.Join(t.TempDir(), "proj")
	wt := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"-C", main, "init", "-q"},
		{"-C", main, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"},
		{"-C", main, "worktree", "add", "-q", wt}} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	for _, k := range []string{"HERDR_PLANNER_NAME", "HERDR_BUILDER_NAME", "HERDR_REVIEWER_NAME"} {
		t.Setenv(k, "")
	}
	home := filepath.Join(t.TempDir(), "state")
	doctorProbe = okProbe{}
	out, _ := captureStdoutRC(t, func() int {
		return cmdDoctor([]string{"--cwd", wt, "--state-home", home, "--builder", "claude", "--reviewer", "claude"})
	})
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "with the id assigned on the first tandem-pair") {
		t.Fatalf("doctor did not report the pending id:\n%s", out)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("doctor created the state home: %v", err)
	}
}
