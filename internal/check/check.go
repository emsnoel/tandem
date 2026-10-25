// Package check runs a project's check command with its output in a log file
// and summarises the result in a few lines: the exit code, the lines naming a
// failure, and the tail where test runners print totals. A builder that ran the
// check in its own tool would keep every line in its context.
package check

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// TimeoutExit is the exit code recorded for a check stopped by its deadline,
// the same number timeout(1) uses.
const TimeoutExit = 124

const (
	maxMatches = 30
	tailLines  = 15
)

// Result is one check run.
type Result struct {
	Exit     int
	Duration time.Duration
	Log      string
	TimedOut bool
}

// Run executes command through bash in root, with extra appended as its
// arguments ("$@"), writing stdout and stderr to logPath. A non-zero exit is
// a Result, not an error; err is for a check that could not be started.
func Run(ctx context.Context, root, command string, extra []string, logPath string) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return Result{}, err
	}
	f, err := os.Create(logPath)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	args := append([]string{"-c", command + ` "$@"`, "check"}, extra...)
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Dir = root
	cmd.Stdout = f
	cmd.Stderr = f
	// Kill the whole group on timeout: a check is usually a chain of
	// processes, and killing only bash leaves the test runner holding the log.
	setProcessGroup(cmd)
	start := time.Now()
	err = cmd.Run()
	r := Result{Duration: time.Since(start), Log: logPath}
	if ctx.Err() == context.DeadlineExceeded {
		r.TimedOut, r.Exit = true, TimeoutExit
		fmt.Fprintf(f, "\n[tandem check] stopped after %s: the check timed out\n", r.Duration.Round(time.Second))
		return r, nil
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		r.Exit = exit.ExitCode()
	default:
		return Result{}, err
	}
	return r, nil
}

var failRE = regexp.MustCompile(`(?i)\b(fail|failed|failure|error|errors)\b|✕`)

// Summary is what a builder is shown instead of the log.
func Summary(r Result, logText string) string {
	var b strings.Builder
	status := fmt.Sprintf("exit %d", r.Exit)
	if r.TimedOut {
		status = "timed out"
	}
	fmt.Fprintf(&b, "check: %s in %s\n", status, r.Duration.Round(time.Second))
	fmt.Fprintf(&b, "log: %s\n", r.Log)
	lines := strings.Split(strings.TrimRight(logText, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	tailFrom := len(lines) - tailLines
	if r.Exit == 0 {
		tailFrom = len(lines) - 3
	}
	if tailFrom < 0 {
		tailFrom = 0
	}
	if r.Exit != 0 {
		var hits []string
		for _, l := range lines[:tailFrom] {
			if failRE.MatchString(l) {
				hits = append(hits, l)
				if len(hits) == maxMatches {
					break
				}
			}
		}
		if len(hits) > 0 {
			b.WriteString("failing lines:\n")
			for _, l := range hits {
				b.WriteString("  " + l + "\n")
			}
		}
	}
	if tailFrom < len(lines) {
		b.WriteString("last lines:\n")
		for _, l := range lines[tailFrom:] {
			b.WriteString("  " + l + "\n")
		}
	}
	return b.String()
}
