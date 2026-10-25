package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/workflow"
)

// fixtureCLI writes an executable shell script that stands in for an agent CLI.
func fixtureCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture-cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// enabled is a registry that supports the fixture kind at one version, with the
// whole capability set, and runs the fixture script after the launch arguments.
func enabled(script string) Reviewer {
	return Reviewer{
		Entries: map[string]DirectEntry{"fixture": {
			Caps:     ReviewCapabilities{Direct: true, StructuredResult: true, Cancellation: true},
			Versions: []string{"fixture 1.2"},
			Evidence: "test fixture",
			Headless: func(launch []string) []string { return append([]string{script}, launch...) },
		}},
		Version: func(string) string { return "fixture 1.2.7" },
		Grace:   300 * time.Millisecond,
	}
}

func request(prompt string, launch ...string) DirectRequest {
	return DirectRequest{Kind: "fixture", JobID: strings.Repeat("1", 32), Launch: launch, Prompt: prompt, Timeout: 10 * time.Second}
}

func TestDirectReviewUnsupportedFallsBack(t *testing.T) {
	// What ships: every registered kind, no capability, pane execution.
	for _, kind := range append(agent.Kinds(), "nonesuch", "") {
		if c := Capabilities(kind); c != (ReviewCapabilities{}) {
			t.Errorf("%s: %+v; nothing may be enabled until its adapter passes the fixtures and a live check", kind, c)
		}
		if m := (Reviewer{Version: func(string) string { return "1.0" }}).Mode(kind); m != ModePane {
			t.Errorf("%s: mode %s", kind, m)
		}
		if m := DefaultReviewer.Mode(kind); m != ModePane {
			t.Errorf("%s: the default reviewer picked %s", kind, m)
		}
	}
	if len(DefaultReviewer.Entries) != 0 {
		t.Errorf("%d kinds are enabled in the shipped registry: enabling one needs verified evidence and this test changed with it", len(DefaultReviewer.Entries))
	}
	if _, err := DefaultReviewer.Run(context.Background(), request("x")); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("the default reviewer ran something: %v", err)
	}
	if h, err := StartReview(context.Background(), workflow.Job{ID: strings.Repeat("1", 32), Kind: "reviewer", RunnerKind: "direct-codex"}); !errors.Is(err, workflow.ErrUnsupported) || h != "" {
		t.Errorf("StartReview: %q %v", h, err)
	}

	// A partly verified adapter is still pane: all three capabilities, a version
	// that matches a supported family, and a version that can be read.
	full := enabled("/bin/true")
	if m := full.Mode("fixture"); m != ModeDirect {
		t.Fatalf("a fully verified fixture: %s", m)
	}
	for name, mut := range map[string]func(*Reviewer){
		"no direct": func(r *Reviewer) { e := r.Entries["fixture"]; e.Caps.Direct = false; r.Entries["fixture"] = e },
		"no result": func(r *Reviewer) {
			e := r.Entries["fixture"]
			e.Caps.StructuredResult = false
			r.Entries["fixture"] = e
		},
		"no cancel":         func(r *Reviewer) { e := r.Entries["fixture"]; e.Caps.Cancellation = false; r.Entries["fixture"] = e },
		"other version":     func(r *Reviewer) { r.Version = func(string) string { return "fixture 2.0.1" } },
		"prefix only":       func(r *Reviewer) { r.Version = func(string) string { return "fixture 1.20" } },
		"unknown version":   func(r *Reviewer) { r.Version = func(string) string { return "" } },
		"no version source": func(r *Reviewer) { r.Version = nil },
		"no command":        func(r *Reviewer) { e := r.Entries["fixture"]; e.Headless = nil; r.Entries["fixture"] = e },
		"no evidence":       func(r *Reviewer) { e := r.Entries["fixture"]; e.Evidence = ""; r.Entries["fixture"] = e },
	} {
		r := enabled("/bin/true")
		mut(&r)
		if m := r.Mode("fixture"); m != ModePane {
			t.Errorf("%s: %s", name, m)
		}
		if _, err := r.Run(context.Background(), request("x")); !errors.Is(err, workflow.ErrUnsupported) {
			t.Errorf("%s: ran anyway: %v", name, err)
		}
	}
}

func TestDirectReviewPreservesPermissions(t *testing.T) {
	script := fixtureCLI(t, `for a in "$@"; do printf '%s\n' "$a"; done`)
	// Every real adapter's resolved launch arguments, at every autonomy it
	// supports, reach the direct command exactly as resolved: not widened, not
	// narrowed, not reordered.
	for _, kind := range agent.Kinds() {
		a, _ := agent.Get(kind)
		for _, level := range a.Supports("autonomy") {
			launch, err := a.Resolve(agent.Spec{Kind: kind, Role: agent.RoleBuilder, Autonomy: agent.Autonomy(level)})
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, level, err)
			}
			out, err := enabled(script).Run(context.Background(), request("prompt", launch.Args...))
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, level, err)
			}
			got := strings.Split(strings.TrimRight(string(out.Stdout), "\n"), "\n")
			if len(launch.Args) == 0 {
				continue
			}
			if strings.Join(got, "\x00") != strings.Join(launch.Args, "\x00") {
				t.Errorf("%s/%s: asked for %q, the command received %q", kind, level, launch.Args, got)
			}
		}
	}
}

func TestDirectReviewFramesThePromptOnStdinNotTheCommandLine(t *testing.T) {
	script := fixtureCLI(t, `printf 'ARGS:%s\n' "$#"; cat`)
	prompt := "line one\n'quoted' \"double\" $HOME `backtick` ; rm -rf /\n日本語 \\n tab\there\n"
	out, err := enabled(script).Run(context.Background(), request(prompt, "--flag"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out.Stdout); got != "ARGS:1\n"+prompt {
		t.Errorf("the prompt did not arrive byte for byte on stdin:\n%q", got)
	}
	if out.ExitCode != 0 || out.Handle == "" {
		t.Errorf("%+v", out)
	}
}

func TestDirectReviewRunsInAnIsolatedWorkingDirectory(t *testing.T) {
	script := fixtureCLI(t, `pwd; ls -A | wc -l`)
	here, _ := os.Getwd()
	out, err := enabled(script).Run(context.Background(), request("x"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out.Stdout)), "\n")
	cwd := lines[0]
	if strings.HasPrefix(cwd, here) || cwd == "/" || strings.TrimSpace(lines[1]) != "0" {
		t.Errorf("working directory %q with %s entries; it must be a fresh empty directory away from the checkout", cwd, lines[1])
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("the job directory %s was left behind", cwd)
	}
}

func TestDirectReviewReportsExitsOutputLimitsAndTimeouts(t *testing.T) {
	r := enabled(fixtureCLI(t, `echo partial; echo "it broke" >&2; exit 7`))
	out, err := r.Run(context.Background(), request("x"))
	if err != nil || out.ExitCode != 7 || !strings.Contains(out.Stderr, "it broke") || string(out.Stdout) != "partial\n" {
		t.Errorf("a nonzero exit is an outcome, not an error: %+v %v", out, err)
	}

	r = enabled(fixtureCLI(t, `head -c 2000000 /dev/zero | tr '\0' 'a'; echo; echo "e" | head -c 100 >&2`))
	out, err = r.Run(context.Background(), request("x"))
	if err != nil || !out.Truncated || len(out.Stdout) > MaxDirectOutput+1 {
		t.Errorf("output beyond the limit must be marked, and not held: truncated=%v %d bytes %v", out.Truncated, len(out.Stdout), err)
	}

	r = enabled(fixtureCLI(t, `sleep 30`))
	start := time.Now()
	q := request("x")
	q.Timeout = 300 * time.Millisecond
	out, err = r.Run(context.Background(), q)
	if err != nil || !out.TimedOut || time.Since(start) > 5*time.Second {
		t.Errorf("a reviewer that never finishes: %+v %v after %v", out, err, time.Since(start))
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestDirectReviewCancellationStopsChildren(t *testing.T) {
	for name, body := range map[string]string{
		"a child that exits when asked": `sleep 300 &
echo $! > "$PIDFILE"
wait`,
		"a child that ignores the request": `trap '' TERM
( trap '' TERM; sleep 300 ) &
echo $! > "$PIDFILE"
wait`,
		"a grandchild in its own shell": `sh -c 'sleep 300 & echo $! > "$PIDFILE"; wait' &
wait`,
	} {
		pidfile := filepath.Join(t.TempDir(), "pid")
		t.Setenv("PIDFILE", pidfile)
		r := enabled(fixtureCLI(t, body))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan DirectOutcome, 1)
		go func() {
			out, _ := r.Run(ctx, request("x"))
			done <- out
		}()
		var child int
		for i := 0; i < 100 && child == 0; i++ {
			time.Sleep(50 * time.Millisecond)
			if raw, err := os.ReadFile(pidfile); err == nil {
				child, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			}
		}
		if child == 0 || !alive(child) {
			t.Fatalf("%s: the fixture did not start a child", name)
		}
		cancel()
		select {
		case out := <-done:
			if !out.Cancelled {
				t.Errorf("%s: %+v", name, out)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: cancellation did not return", name)
		}
		// Every process the job owned is gone, however it was started.
		gone := false
		for i := 0; i < 40 && !gone; i++ {
			gone = !alive(child)
			time.Sleep(50 * time.Millisecond)
		}
		if !gone {
			syscall.Kill(child, syscall.SIGKILL)
			t.Errorf("%s: process %d outlived its job", name, child)
		}
	}
}

func TestDirectReviewKillsLeftoverChildrenAfterANormalExit(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("PIDFILE", pidfile)
	r := enabled(fixtureCLI(t, `sleep 300 >/dev/null 2>&1 &
echo $! > "$PIDFILE"
echo '{}'`))
	if _, err := r.Run(context.Background(), request("x")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(pidfile)
	child, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	for i := 0; i < 40 && alive(child); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(child) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Error("a background process outlived a review that finished")
	}
}

func TestDirectReviewNeedsARealCommand(t *testing.T) {
	r := enabled("/nonexistent/cli")
	if _, err := r.Run(context.Background(), request("x")); err == nil || errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("a command that cannot start is an error to report: %v", err)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
}
