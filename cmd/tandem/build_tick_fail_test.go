package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
)

// setRunState writes a run's state straight into the store, as a pause from an
// earlier tick leaves it.
func (e *cliEnv) setRunState(run, st, code string) {
	e.t.Helper()
	id, _, err := state.LookupIdentity(e.root, e.home)
	if err != nil {
		e.t.Fatal(err)
	}
	s, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		e.t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetRunState(context.Background(), run, st, code); err != nil {
		e.t.Fatal(err)
	}
}

// nestedRepo puts an untracked git repository in the checkout. Tandem cannot
// capture such a tree exactly, so every step that needs it reports busy.
func (e *cliEnv) nestedRepo() {
	e.t.Helper()
	nested := filepath.Join(e.root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if out, err := gitCmd(nested, "init", "-q"); err != nil {
		e.t.Fatalf("%v %s", err, out)
	}
}

// A tick that fails on a paused run still prints the run's fields, so the
// bridge can tell the planner about the pause. The exit stays non-zero.
func TestTickFieldsWhenTheTickFails(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, started, _ := e.start()
	run := started["run_id"].(string)
	e.sub("tick")
	e.setRunState(run, "paused", "builder_silent")
	durableTransport = func(transportSpec) (runner.Transport, error) { return nil, errors.New("herdr is not answering") }
	for _, verb := range []string{"tick", "recover"} {
		rc, out, errOut := e.sub(verb, "--fields")
		if rc == exitcode.OK || !strings.Contains(errOut, "herdr is not answering") {
			t.Fatalf("%s: rc %d, stderr %s", verb, rc, errOut)
		}
		if f := nulFields(t, out); !reflect.DeepEqual(f, []string{run, "paused", "builder_silent", ""}) {
			t.Fatalf("%s: fields %q", verb, f)
		}
	}
}

// goneTransport is the CLI transport with a host that says one agent is gone.
type goneTransport struct {
	*cliTransport
	gone string
}

func (g goneTransport) ObserveAgent(_ context.Context, name string) (runner.Observation, error) {
	if name == g.gone {
		return runner.Observation{}, fmt.Errorf("%w: %s", runner.ErrAgentGone, name)
	}
	return runner.Observation{State: runner.ObsCompleted}, nil
}

// stuckHandback leaves a running run whose builder took its task and handed
// back over a tree tandem cannot capture, so every tick fails as busy.
func (e *cliEnv) stuckHandback() string {
	e.t.Helper()
	_, started, _ := e.start()
	run := started["run_id"].(string)
	e.sub("tick")
	if e.tr.count() != 1 {
		e.t.Fatalf("task prompts: %d", e.tr.count())
	}
	id, _, err := state.LookupIdentity(e.root, e.home)
	if err != nil {
		e.t.Fatal(err)
	}
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		e.t.Fatal(err)
	}
	prompt, err := art.ReadAll(context.Background(), e.tr.sends[0].PromptHash, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	ident := []string{"--task", "1"}
	for _, f := range []string{"attempt", "job", "effect", "nonce"} {
		m := regexp.MustCompile(`--` + f + ` ([0-9a-f]+)`).FindSubmatch(prompt)
		if m == nil {
			e.t.Fatalf("no --%s in the task prompt", f)
		}
		ident = append(ident, "--"+f, string(m[1]))
	}
	event := func(kind string, extra ...string) {
		args := append([]string{"build", "event", "new", "--root", e.root, "--kind", kind}, ident...)
		if rc, _, errOut := e.run(append(args, extra...)...); rc != exitcode.OK {
			e.t.Fatalf("%s: rc %d %s", kind, rc, errOut)
		}
	}
	event("builder_started")
	if rc, _, errOut := e.sub("tick"); rc != exitcode.OK {
		e.t.Fatalf("tick: rc %d %s", rc, errOut)
	}
	e.nestedRepo()
	report := filepath.Join(e.t.TempDir(), "report.md")
	os.WriteFile(report, []byte("done\n"), 0o644)
	event("builder_submitted", "--payload", report)
	return run
}

// A tick that keeps failing still looks for a gone pane, and the pause it makes
// is printed with the failure.
func TestFailingTickStillPausesForAGonePane(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	run := e.stuckHandback()
	rc, out, errOut := e.sub("tick", "--fields")
	if rc == exitcode.OK || !strings.Contains(errOut, "cannot be captured exactly") {
		t.Fatalf("the tick should fail on the nested repository: rc %d, stderr %s", rc, errOut)
	}
	if f := nulFields(t, out); !reflect.DeepEqual(f, []string{run, "running", "", ""}) {
		t.Fatalf("fields %q", f)
	}
	durableTransport = func(transportSpec) (runner.Transport, error) {
		return goneTransport{cliTransport: e.tr, gone: "reviewer"}, nil
	}
	rc, out, errOut = e.sub("tick", "--observe-agents", "--fields")
	if rc == exitcode.OK || !strings.Contains(errOut, "cannot be captured exactly") {
		t.Fatalf("rc %d, stderr %s", rc, errOut)
	}
	if f := nulFields(t, out); !reflect.DeepEqual(f, []string{run, "paused", "agent_exited", ""}) {
		t.Fatalf("fields %q, stderr %s", f, errOut)
	}
}
