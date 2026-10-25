package runner

// Direct reviewer execution, behind capabilities that are all off.
//
// A review normally runs in a pane and its result is recorded
// (controller.RecordReviewRaw). A CLI that can run to completion as a separate
// process would let the controller own the whole job. No adapter is enabled,
// because no CLI's headless flags, output framing, cancel behaviour and
// permissions are verified, and none are guessed.
//
// Enabling a kind must bring its evidence: a verified version family and the
// pure function that frames its launch arguments into a command. Until then
// Mode says pane.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// ReviewCapabilities say what a kind's CLI can be trusted to do when run
// directly. All three must hold, for the installed version, before a review
// runs that way.
type ReviewCapabilities struct {
	// Direct: it can run a review to completion as a separate process.
	Direct bool
	// StructuredResult: it prints the review as one parseable object, framed so
	// the controller can tell a result from noise.
	StructuredResult bool
	// Cancellation: stopping it, and everything it started, is reliable.
	Cancellation bool
}

func (c ReviewCapabilities) all() bool { return c.Direct && c.StructuredResult && c.Cancellation }

// ReviewMode is where a review runs.
type ReviewMode string

const (
	ModePane   ReviewMode = "pane"
	ModeDirect ReviewMode = "direct"
)

// DirectEntry is one kind's verified direct-execution support.
type DirectEntry struct {
	Caps ReviewCapabilities
	// Versions are the CLI version families this was verified against. A version
	// matches a family when it equals it or continues it after a dot, so "1.2"
	// covers "1.2.7" but not "1.20".
	Versions []string
	// Evidence says where and when it was verified.
	Evidence string
	// Headless frames the adapter's resolved launch arguments, which carry the
	// autonomy, model and effort settings unchanged, into the argv of a headless
	// review. It is pure and adds only what was verified.
	Headless func(launch []string) []string
}

// Reviewer decides where a review runs and runs it directly when it may.
type Reviewer struct {
	Entries map[string]DirectEntry
	// Version reports the installed CLI version of a kind, "" when it cannot be
	// read. Without it nothing is direct: capability is per version.
	Version func(kind string) string
	// Grace is how long a process gets to stop when asked before the whole
	// process group is killed; 2s when zero.
	Grace time.Duration
}

// DefaultReviewer is what ships: no kind is enabled.
var DefaultReviewer = Reviewer{}

// Capabilities returns the capabilities enabled for kind in the shipped
// registry, ignoring versions: all false.
func Capabilities(kind string) ReviewCapabilities { return DefaultReviewer.Entries[kind].Caps }

// Mode is direct only when every capability holds, the entry names its
// evidence and its command, and the installed version is in a verified family.
func (r Reviewer) Mode(kind string) ReviewMode {
	e, ok := r.Entries[kind]
	if !processGroups || !ok || !e.Caps.all() || e.Evidence == "" || e.Headless == nil || r.Version == nil {
		return ModePane
	}
	v := r.Version(kind)
	for _, fam := range e.Versions {
		if v != "" && (v == fam || strings.HasPrefix(v, fam+".")) {
			return ModeDirect
		}
	}
	return ModePane
}

// StartReview is the job-level entry point. It refuses unless the job's kind is
// enabled, which in the shipped registry none is.
func StartReview(ctx context.Context, job workflow.Job) (string, error) {
	kind, ok := strings.CutPrefix(job.RunnerKind, "direct-")
	if !ok || !DefaultReviewer.Entries[kind].Caps.all() {
		return "", fmt.Errorf("%w: job %s (%s) has no verified direct execution; it runs in a pane", workflow.ErrUnsupported, job.ID, job.RunnerKind)
	}
	return "", fmt.Errorf("%w: a direct review needs its request; use Reviewer.Run", workflow.ErrUnsupported)
}

// MaxDirectOutput is the most of a reviewer's output that is kept.
const MaxDirectOutput = 1 << 20

const maxDirectStderr = 64 << 10

// DirectRequest is one review to run.
type DirectRequest struct {
	Kind  string
	JobID string
	// Launch are the arguments the adapter resolved for the builder's settings.
	Launch []string
	// Prompt is the review, delivered on stdin: never on the command line, where
	// quoting and length would decide what the reviewer is told.
	Prompt  string
	Timeout time.Duration
	// Env is added to the inherited environment.
	Env []string
}

// DirectOutcome is what happened. A nonzero exit is an outcome, not an error.
type DirectOutcome struct {
	// Handle names the process group the review ran in.
	Handle    string
	Stdout    []byte
	Stderr    string
	ExitCode  int
	Cancelled bool
	TimedOut  bool
	// Truncated: the output exceeded MaxDirectOutput. What was kept is not a
	// complete result.
	Truncated bool
	Duration  time.Duration
}

// Run runs the review as a separate process in its own process group, in an
// empty directory of its own. It returns when the process has exited, or has
// been stopped on cancellation or timeout together with everything it started.
func (r Reviewer) Run(ctx context.Context, req DirectRequest) (DirectOutcome, error) {
	if r.Mode(req.Kind) != ModeDirect {
		return DirectOutcome{}, fmt.Errorf("%w: %s has no verified direct review for this version; it runs in a pane", workflow.ErrUnsupported, req.Kind)
	}
	argv := r.Entries[req.Kind].Headless(append([]string{}, req.Launch...))
	if len(argv) == 0 {
		return DirectOutcome{}, fmt.Errorf("direct review of %s has no command", req.Kind)
	}
	dir, err := os.MkdirTemp("", "tandem-review-*")
	if err != nil {
		return DirectOutcome{}, err
	}
	defer os.RemoveAll(dir)

	grace := r.Grace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	stdout, stderr := &capped{max: MaxDirectOutput}, &capped{max: maxDirectStderr, tail: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = grace
	inOwnGroup(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return DirectOutcome{}, fmt.Errorf("starting the %s reviewer: %w", req.Kind, err)
	}
	pid := cmd.Process.Pid
	out := DirectOutcome{Handle: fmt.Sprintf("pgid:%d", pid)}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeout <-chan time.Time
	if req.Timeout > 0 {
		t := time.NewTimer(req.Timeout)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-done:
	case <-ctx.Done():
		out.Cancelled = true
		stopGroup(pid, grace, done)
	case <-timeout:
		out.TimedOut = true
		stopGroup(pid, grace, done)
	}
	// Whatever the leader left behind goes with it.
	killGroup(pid)

	out.Duration = time.Since(start)
	out.Stdout, out.Truncated = stdout.bytes(), stdout.over
	out.Stderr = string(stderr.bytes())
	out.ExitCode = -1
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}
	return out, nil
}

// capped keeps at most max bytes (the first, or with tail the last) and discards
// the rest, always reporting a full write so the process is never blocked.
type capped struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	max  int
	tail bool
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if c.tail {
		c.buf.Write(p)
		if c.buf.Len() > c.max {
			c.over = true
			b := c.buf.Bytes()
			c.buf = *bytes.NewBuffer(append([]byte{}, b[len(b)-c.max:]...))
		}
		return n, nil
	}
	if room := c.max + 1 - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	if c.buf.Len() > c.max {
		c.over = true
	}
	return n, nil
}

func (c *capped) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte{}, c.buf.Bytes()...)
}
