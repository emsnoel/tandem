package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/runner"
)

// paneTransport is cliTransport with panes that answer, by name. A pane nobody
// named is idle.
type paneTransport struct {
	*cliTransport
	panes map[string]runner.Observation
	gone  map[string]bool
}

func (p *paneTransport) ObserveAgent(_ context.Context, name string) (runner.Observation, error) {
	if p.gone[name] {
		return runner.Observation{}, fmt.Errorf("%w: %s", runner.ErrAgentGone, name)
	}
	if o, ok := p.panes[name]; ok {
		return o, nil
	}
	return runner.Observation{State: runner.ObsCompleted}, nil
}

func (p *paneTransport) Observe(ctx context.Context, _ string) (runner.Observation, error) {
	return p.ObserveAgent(ctx, "builder")
}

// paneCLI is a started run with its first prompt typed and never
// acknowledged, and panes the test controls.
func paneCLI(t *testing.T) (*cliEnv, *paneTransport, string) {
	t.Helper()
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	pt := &paneTransport{cliTransport: e.tr, panes: map[string]runner.Observation{}, gone: map[string]bool{}}
	old := durableTransport
	durableTransport = func(transportSpec) (runner.Transport, error) { return pt, nil }
	t.Cleanup(func() { durableTransport = old })
	rc, got, errOut := e.start()
	if rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	if rc, out, errOut := e.sub("tick"); rc != exitcode.OK {
		t.Fatalf("tick: %d %s %s", rc, out, errOut)
	}
	return e, pt, got["run_id"].(string)
}

func (e *cliEnv) state(verb string, extra ...string) (int, string, string) {
	e.t.Helper()
	return e.run(append([]string{"state", verb, "--root", e.root, "--state-home", e.home}, extra...)...)
}

func (e *cliEnv) stopAndWait(want int) {
	e.t.Helper()
	if rc, out, errOut := e.sub("stop"); rc != exitcode.OK {
		e.t.Fatalf("stop: %d %s %s", rc, out, errOut)
	}
	if rc, out, errOut := e.sub("wait", "--until", "paused", "--tick", "--timeout", "1500"); rc != want {
		e.t.Fatalf("wait: %d, want %d: %s %s", rc, want, out, errOut)
	}
}

func TestSettleDeliveryCLINeedsTheOwnersStatement(t *testing.T) {
	e, _, run := paneCLI(t)
	eff := e.attempt("task-1").effect
	for name, args := range map[string][]string{
		"no --abandon": {"--run", run, "--effect", eff, "--because", "why"},
		"no --because": {"--run", run, "--effect", eff, "--abandon"},
		"blank reason": {"--run", run, "--effect", eff, "--abandon", "--because", "  "},
		"no --effect":  {"--run", run, "--abandon", "--because", "why"},
		"no --run":     {"--effect", eff, "--abandon", "--because", "why"},
	} {
		if rc, _, _ := e.state("settle-delivery", args...); rc != exitcode.Usage {
			t.Errorf("%s: exit %d", name, rc)
		}
	}
	if got := e.effectState(eff); got != "delivering" {
		t.Fatalf("the prompt is %s", got)
	}
}

// A busy builder may be working on the prompt, so the owner cannot abandon it.
// A later idle pane lets the same command through.
func TestSettleDeliveryCLIRefusesAWorkingPane(t *testing.T) {
	e, pt, run := paneCLI(t)
	eff := e.attempt("task-1").effect
	e.stopAndWait(exitcode.OK)
	settle := []string{"--run", run, "--effect", eff, "--abandon", "--because", "never shown"}

	pt.panes["builder"] = runner.Observation{State: runner.ObsRunning}
	rc, out, errOut := e.state("settle-delivery", settle...)
	if rc != exitcode.ManualGate || out != "" || !strings.Contains(errOut, "builder") || !strings.Contains(errOut, "working") {
		t.Fatalf("rc %d out %q err %q", rc, out, errOut)
	}
	if got := e.effectState(eff); got != "delivering" {
		t.Fatalf("a refusal moved the prompt to %s", got)
	}
	var n int
	if err := e.stateDB().QueryRow(`SELECT count(*) FROM events WHERE kind='delivery_abandoned'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d statements after a refusal (%v)", n, err)
	}

	delete(pt.panes, "builder")
	rc, out, errOut = e.state("settle-delivery", settle...)
	if rc != exitcode.OK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	res := oneJSON(t, out)
	if res["effect_id"] != eff || res["state"] != "cancelled" {
		t.Fatalf("output %s", out)
	}
}

func TestSettleDeliveryCLIRefusesWhileTheWriterIsHeld(t *testing.T) {
	e, _, run := paneCLI(t)
	eff := e.attempt("task-1").effect
	settle := []string{"--run", run, "--effect", eff, "--abandon", "--because", "never shown"}
	if rc, _, errOut := e.state("settle-delivery", settle...); rc != exitcode.ManualGate || !strings.Contains(errOut, "tandem build stop") {
		t.Fatalf("a running run: rc %d %s", rc, errOut)
	}
	if _, err := e.stateDBWritable().Exec(`UPDATE runs SET state='paused', pause_code='writer_unknown' WHERE id=?`, run); err != nil {
		t.Fatal(err)
	}
	rc, _, errOut := e.state("settle-delivery", settle...)
	if rc != exitcode.ManualGate || !strings.Contains(errOut, "writer lease") || !strings.Contains(errOut, "tandem build stop") {
		t.Fatalf("a held lease: rc %d %s", rc, errOut)
	}
	if got := e.effectState(eff); got != "delivering" {
		t.Fatalf("the prompt is %s", got)
	}
}

// settleCommand is the fix-it line in an archive refusal.
var settleCommand = regexp.MustCompile(`tandem (state settle-delivery --run \S+ --effect \S+ --abandon --because WHY --root DIR)`)

// The restart route exactly as a note prints it, on a run whose prompt nobody
// confirmed: the archive refuses, and the command in the refusal gets it through.
func TestRestartRouteAsPrintedEndsAnOpenDelivery(t *testing.T) {
	e, _, run := paneCLI(t)
	e.stopAndWait(exitcode.OK)
	if rc, out, errOut := e.state("recover", "--run", run); rc != exitcode.OK {
		t.Fatalf("recover: %d %s %s", rc, out, errOut)
	}
	rc, _, errOut := e.state("archive", "--run", run)
	m := settleCommand.FindStringSubmatch(errOut)
	if rc != exitcode.ManualGate || m == nil {
		t.Fatalf("archive: rc %d %q", rc, errOut)
	}
	args := strings.Fields(strings.NewReplacer("WHY", "never-shown", "DIR", e.root).Replace(m[1]))
	if rc, out, errOut := e.run(append(args, "--state-home", e.home)...); rc != exitcode.OK {
		t.Fatalf("%v: %d %s %s", args, rc, out, errOut)
	}
	if rc, out, errOut := e.state("archive", "--run", run); rc != exitcode.OK {
		t.Fatalf("archive: %d %s %s", rc, out, errOut)
	}
	sent := e.tr.count()
	if rc, _, errOut := e.start(); rc != exitcode.OK {
		t.Fatalf("a new run: %d %s", rc, errOut)
	}
	if rc, out, errOut := e.sub("tick"); rc != exitcode.OK {
		t.Fatalf("tick: %d %s %s", rc, out, errOut)
	}
	if e.tr.count() != sent+1 {
		t.Fatalf("the new run typed %d prompts", e.tr.count()-sent)
	}
}

// A gone builder pane keeps the wait from ending, and bringing the pane back
// finishes the stop with the same command. This is what the notes print.
func TestBuildWaitTicksAStopToPausedOnceTheBuilderIsBack(t *testing.T) {
	e, pt, run := paneCLI(t)
	pt.gone["builder"] = true
	e.stopAndWait(exitcode.ManualGate)
	if st, _ := e.runState(); st != "stopping" {
		t.Fatalf("the run is %s", st)
	}
	eff := e.attempt("task-1").effect
	if rc, _, errOut := e.state("settle-delivery", "--run", run, "--effect", eff, "--abandon", "--because", "gone"); rc != exitcode.ManualGate {
		t.Fatalf("settling a stop that holds the writer: %d %s", rc, errOut)
	}
	delete(pt.gone, "builder")
	if rc, out, errOut := e.sub("wait", "--until", "paused", "--tick", "--timeout", "1500"); rc != exitcode.OK {
		t.Fatalf("wait: %d %s %s", rc, out, errOut)
	}
	if st, code := e.runState(); st != "paused" || code != "stopped" {
		t.Fatalf("the run is %s/%s", st, code)
	}
}

// The scheduling_blocked note prints resume and amend. A stopped collaboration
// is cleared by resume. One held by a ceiling goes straight back to held on
// resume, and is cleared only by amend, so the note has to print both.
func TestSchedulingBlockedNoteCommandsClearIt(t *testing.T) {
	const policy = `{"enforce_budgets":true,"input_ceilings":{"codex":50},"output_ceilings":{"codex":100},"input_reservations":{"codex":%d},"output_reservations":{"codex":5}}`
	hold := func(t *testing.T, reserve int) (*cliEnv, string) {
		t.Helper()
		e := newCLI(t)
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		rc, got, errOut := e.collab("create", "--policy", e.file("policy.json", fmt.Sprintf(policy, reserve)))
		if rc != exitcode.OK {
			t.Fatalf("create: %d %s", rc, errOut)
		}
		id := got["collaboration_id"].(string)
		if rc, _, errOut := e.start("--collaboration", id); rc != exitcode.OK {
			t.Fatalf("start: %d %s", rc, errOut)
		}
		return e, id
	}
	blocked := func(t *testing.T, e *cliEnv) {
		t.Helper()
		e.sub("tick")
		if st, code := e.runState(); st != "paused" || code != "scheduling_blocked" {
			t.Fatalf("the run is %s/%s", st, code)
		}
	}
	t.Run("a stopped collaboration resumes", func(t *testing.T) {
		e, id := hold(t, 10)
		if rc, _, errOut := e.collab("stop", "--collaboration", id, "--actor", "you", "--reason", "hold"); rc != exitcode.OK {
			t.Fatalf("stop: %d %s", rc, errOut)
		}
		blocked(t, e)
		rc, got, errOut := e.collab("resume", "--collaboration", id, "--actor", "you", "--reason", "go")
		if rc != exitcode.OK || got["run_resumed"] != true {
			t.Fatalf("resume: %d %v %s", rc, got, errOut)
		}
		e.sub("tick")
		if st, code := e.runState(); st != "running" {
			t.Fatalf("after the resume the run is %s/%s", st, code)
		}
		if e.tr.count() != 1 {
			t.Fatalf("%d prompts typed", e.tr.count())
		}
	})
	t.Run("a ceiling needs an amendment", func(t *testing.T) {
		e, id := hold(t, 60)
		blocked(t, e)
		if rc, _, errOut := e.collab("resume", "--collaboration", id, "--actor", "you", "--reason", "go"); rc != exitcode.OK {
			t.Fatalf("resume: %d %s", rc, errOut)
		}
		blocked(t, e)
		rc, got, errOut := e.collab("amend", "--collaboration", id, "--provider", "codex", "--input-ceiling", "500", "--actor", "you", "--reason", "room")
		if rc != exitcode.OK || got["run_resumed"] != true {
			t.Fatalf("amend: %d %v %s", rc, got, errOut)
		}
		e.sub("tick")
		if st, code := e.runState(); st != "running" {
			t.Fatalf("after the amendment the run is %s/%s", st, code)
		}
	})
	t.Run("the restart route still ends a held run", func(t *testing.T) {
		e, _ := hold(t, 60)
		blocked(t, e)
		run := oneRun(t, e)
		e.stopAndWait(exitcode.OK)
		if rc, out, errOut := e.state("recover", "--run", run); rc != exitcode.OK {
			t.Fatalf("recover: %d %s %s", rc, out, errOut)
		}
		if rc, out, errOut := e.state("archive", "--run", run); rc != exitcode.OK {
			t.Fatalf("archive: %d %s %s", rc, out, errOut)
		}
	})
}

func oneRun(t *testing.T, e *cliEnv) string {
	t.Helper()
	var id string
	if err := e.stateDB().QueryRow(`SELECT id FROM runs ORDER BY created_ms DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
