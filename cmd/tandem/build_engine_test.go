package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
)

// cliTransport is the transport the durable commands are given in these
// tests. It records every send and answers observations as "unknown".
type cliTransport struct {
	mu    sync.Mutex
	sends []runner.Delivery
	// notSent, when set, makes every send a definite not-sent failure with this
	// error, as a pane whose turn ended at once does.
	notSent error
}

func (c *cliTransport) Send(_ context.Context, d runner.Delivery) (runner.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends = append(c.sends, d)
	if c.notSent != nil {
		return runner.Receipt{}, runner.NotSent(c.notSent)
	}
	return runner.Receipt{EffectID: d.EffectID, ExternalID: "cli"}, nil
}

func (c *cliTransport) Observe(context.Context, string) (runner.Observation, error) {
	return runner.Observation{State: runner.ObsUnknown}, nil
}

func (c *cliTransport) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.sends) }

const cliPlan = "# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst\n\n### Task 2: Second\n\nsecond\n"

type cliEnv struct {
	t          *testing.T
	root, home string
	plan       string
	tr         *cliTransport
}

// newCLI makes a git checkout with a committed plan and installs a transport
// for the durable commands for the length of the test.
func newCLI(t *testing.T) *cliEnv {
	t.Helper()
	root, dir := buildRepo(t, "", 0)
	os.RemoveAll(dir) // no legacy build: this checkout starts fresh
	plan := filepath.Join(root, "plan.md")
	os.WriteFile(plan, []byte(cliPlan), 0o644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "plan"}} {
		if out, err := gitCmd(root, args...); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	e := &cliEnv{t: t, root: root, home: filepath.Join(t.TempDir(), "state-home"), plan: plan, tr: &cliTransport{}}
	old := durableTransport
	durableTransport = func(transportSpec) (runner.Transport, error) { return e.tr, nil }
	t.Cleanup(func() { durableTransport = old })
	return e
}

func gitCmd(root string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	return string(out), err
}

func (e *cliEnv) run(args ...string) (int, string, string) {
	e.t.Helper()
	return runCapture(e.t, args...)
}

func (e *cliEnv) start(extra ...string) (int, map[string]any, string) {
	e.t.Helper()
	args := []string{"build", "start", "--plan", e.plan, "--root", e.root, "--builder", "builder", "--planner", "planner",
		"--builder-kind", "codex", "--check", "true", "--state-home", e.home}
	// A run needs a reviewer pane. A test that names part of one is testing the
	// pane, so it is given no default.
	if !slices.ContainsFunc(extra, func(a string) bool { return a == "--reviewer" || a == "--reviewer-kind" }) {
		args = append(args, "--reviewer", "reviewer", "--reviewer-kind", "claude")
	}
	args = append(args, extra...)
	rc, out, errOut := e.run(args...)
	var got map[string]any
	if rc == exitcode.OK {
		got = oneJSON(e.t, out)
	}
	return rc, got, errOut
}

func (e *cliEnv) sub(verb string, extra ...string) (int, string, string) {
	e.t.Helper()
	return e.run(append([]string{"build", verb, "--root", e.root, "--state-home", e.home}, extra...)...)
}

func TestDurableStartIsOptInAndCreatesNoLegacyRecord(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	// Without the opt-in a start is the legacy start and writes a legacy build.
	rc, out, _ := e.run("build", "start", "--plan", e.plan, "--root", e.root)
	if rc != exitcode.OK || !strings.Contains(out, `"task"`) {
		t.Fatalf("legacy start: rc %d %q", rc, out)
	}
	if _, err := os.Stat(filepath.Join(paths.Relay(e.root), "build", "build.json")); err != nil {
		t.Fatal("the default start is no longer legacy")
	}
	if ar, _ := controller.ReadActiveRun(paths.Relay(e.root)); ar != nil {
		t.Fatal("a legacy start wrote a durable marker")
	}
}

func TestEngineSelectionPersistsAcrossEnvChangeThroughTheCLI(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, got, errOut := e.start()
	if rc != exitcode.OK || got["engine"] != "durable" || len(got["run_id"].(string)) != 32 {
		t.Fatalf("durable start: rc %d %v %s", rc, got, errOut)
	}
	relay := paths.Relay(e.root)
	if _, err := os.Stat(filepath.Join(relay, "build", "build.json")); !os.IsNotExist(err) {
		t.Fatal("a durable start wrote a legacy build.json")
	}
	if ar, _ := controller.ReadActiveRun(relay); ar == nil || ar.RunID != got["run_id"] {
		t.Fatalf("marker %+v", ar)
	}

	// The variable now says legacy, or nothing. The run is durable regardless.
	for _, env := range []string{"legacy", "", "garbage"} {
		t.Setenv("TANDEM_BUILD_ENGINE", env)
		rc, out, errOut := e.sub("status")
		if rc != exitcode.OK || !strings.Contains(out, got["run_id"].(string)) || !strings.Contains(out, "running") {
			t.Fatalf("TANDEM_BUILD_ENGINE=%q: status rc %d %q %q", env, rc, out, errOut)
		}
		// Legacy-only commands refuse a durable run rather than corrupt it.
		for _, verb := range []string{"sent", "review-sent", "advance"} {
			rc, _, errOut := e.run("build", verb, "--root", e.root, "--task", "1")
			if rc == exitcode.OK || !strings.Contains(errOut, "durable") {
				t.Errorf("TANDEM_BUILD_ENGINE=%q: build %s on a durable run: rc %d %q", env, verb, rc, errOut)
			}
		}
	}
	// A second start does not replace the active run.
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if rc, _, errOut := e.start(); rc == exitcode.OK || !strings.Contains(errOut, got["run_id"].(string)) {
		t.Fatalf("a second durable start: rc %d %q", rc, errOut)
	}
}

func TestADurableStartNeedsATransportForItsKindAndCreatesNothingWithout(t *testing.T) {
	e := newCLI(t)
	durableTransport = defaultDurableTransport // what ships
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, _, errOut := e.start("--builder-kind", "nonesuch")
	if rc == exitcode.OK || !strings.Contains(errOut, "nonesuch") {
		t.Fatalf("rc %d %q", rc, errOut)
	}
	if _, err := os.Stat(e.home); !os.IsNotExist(err) {
		t.Fatal("a refused start created durable state")
	}
	if ar, _ := controller.ReadActiveRun(paths.Relay(e.root)); ar != nil {
		t.Fatal("a refused start left a marker")
	}
}

func TestDurableTickRecoverStopAndStatus(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, started, _ := e.start()
	run := started["run_id"].(string)

	// Each call is a new process: the writer lease must carry across them.
	rc, out, errOut := e.sub("tick")
	if rc != exitcode.OK {
		t.Fatalf("tick rc %d %s", rc, errOut)
	}
	if res := oneJSON(t, out); res["run_id"] != run || res["state"] != "running" {
		t.Fatalf("tick %v", res)
	}
	if e.tr.count() != 1 {
		t.Fatalf("%d sends: the first tick sends the builder's task prompt", e.tr.count())
	}
	if _, out, _ := e.sub("tick"); oneJSON(t, out)["state"] != "running" || e.tr.count() != 1 {
		t.Fatalf("a second tick must not resend: %d sends", e.tr.count())
	}
	rc, out, _ = e.sub("status", "--json")
	st := oneJSON(t, out)
	if rc != exitcode.OK || st["run_id"] != run || st["state"] != "running" || st["engine"] != "durable" {
		t.Fatalf("status %v", st)
	}
	if tasks, _ := st["tasks"].([]any); len(tasks) != 2 {
		t.Fatalf("tasks %v", st["tasks"])
	}

	rc, out, _ = e.sub("recover")
	if rc != exitcode.OK || oneJSON(t, out)["state"] != "running" {
		t.Fatalf("recover rc %d %s", rc, out)
	}

	// Another tick running at the same time is skipped, not run twice.
	id, _, _ := state.LookupIdentity(e.root, e.home)
	lk, err := state.OpenLockerNamed(id.StateRoot, "tick-"+run)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lk.TryLockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	if rc, out, _ := e.sub("tick"); rc != exitcode.OK || oneJSON(t, out)["skipped"] != "busy" {
		t.Fatalf("a tick beside a running tick: rc %d %s", rc, out)
	}
	unlock()

	if rc, out, errOut := e.sub("stop"); rc != exitcode.OK || oneJSON(t, out)["state"] != "stopping" {
		t.Fatalf("stop rc %d %q %q", rc, out, errOut)
	}
	if _, out, _ := e.sub("tick"); oneJSON(t, out)["state"] == "" || e.tr.count() != 2 {
		t.Fatalf("the cancel message was not sent: %d sends", e.tr.count())
	}
}

func TestDurableAcceptNeedsAnExplicitWaiver(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	e.start()
	if rc, _, errOut := e.sub("accept", "--task", "1", "--because", "looks fine"); rc != exitcode.Usage || !strings.Contains(errOut, "--waive") {
		t.Fatalf("accept with no scope: rc %d %q", rc, errOut)
	}
	if rc, _, _ := e.sub("accept", "--task", "1", "--waive", "everything", "--because", "x"); rc != exitcode.Usage {
		t.Fatalf("an unknown scope: rc %d", rc)
	}
	if rc, _, _ := e.sub("accept", "--task", "1", "--waive", "review", "--because", "  "); rc != exitcode.Usage {
		t.Fatalf("a blank reason: rc %d", rc)
	}
	// A valid request for a task that has nothing submitted has nothing to waive for.
	if rc, _, errOut := e.sub("accept", "--task", "1", "--waive", "review", "--because", "I read it"); rc == exitcode.OK {
		t.Fatalf("a waiver for an attempt with no end snapshot: %q", errOut)
	}
}

// Status prints tasks as task-1, task-2; accept takes that form or the number.
func TestDurableAcceptTakesTheTaskAsStatusPrintsIt(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	e.start()
	_, _, byNumber := e.sub("accept", "--task", "1", "--waive", "review", "--because", "I read it")
	rc, _, byID := e.sub("accept", "--task", "task-1", "--waive", "review", "--because", "I read it")
	if rc == exitcode.Usage || byID != byNumber {
		t.Fatalf("--task task-1: rc %d %q; want what --task 1 says: %q", rc, byID, byNumber)
	}
	for _, bad := range []string{"task-", "task-x", "0", "task-0", "two"} {
		if rc, _, _ := e.sub("accept", "--task", bad, "--waive", "review", "--because", "x"); rc != exitcode.Usage {
			t.Errorf("--task %q: rc %d, want usage", bad, rc)
		}
	}
}

func TestLegacyRelayIgnoresTheDurableOptIn(t *testing.T) {
	root, dir := buildRepo(t, "", 0)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, out, _ := runCapture(t, "build", "status", "--root", root)
	if rc != exitcode.OK || !strings.Contains(out, "state:") || strings.Contains(out, "engine") {
		t.Fatalf("a legacy build under TANDEM_BUILD_ENGINE=durable: rc %d %q", rc, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.json")); err != nil {
		t.Fatal(err)
	}
	// And durable-only verbs say there is no durable run.
	for _, verb := range []string{"tick", "recover"} {
		if rc, _, errOut := runCapture(t, "build", verb, "--root", root); rc == exitcode.OK || !strings.Contains(errOut, "durable") {
			t.Errorf("build %s on a legacy relay: rc %d %q", verb, rc, errOut)
		}
	}
}

func TestBuildEngineUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"tick needs a root":    {"build", "tick"},
		"recover needs a root": {"build", "recover"},
		"tick unknown flag":    {"build", "tick", "--root", "/x", "--nope"},
		"stop unknown flag":    {"build", "start", "--nope"},
	} {
		if rc, out, _ := runCapture(t, args...); rc != exitcode.Usage || out != "" {
			t.Errorf("%s: rc %d stdout %q", name, rc, out)
		}
	}
}

func TestBuildReviewRecordRefusesWhatIsNotOneReviewResult(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if rc, _, errOut := e.start(); rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	good := `{"schema_version":1,"job_id":"` + strings.Repeat("a", 32) + `","attempt_id":"` + strings.Repeat("b", 32) +
		`","patch_hash":"` + strings.Repeat("c", 64) + `","tree_hash":"` + strings.Repeat("d", 64) + `","status":"passed","findings":[]}`
	for name, c := range map[string]struct{ body, want string }{
		"not json":            {"the review passed", "review result"},
		"two objects":         {good + good, "one JSON object"},
		"trailing prose":      {good + "\nLGTM", "one JSON object"},
		"unknown field":       {strings.Replace(good, `"findings":[]`, `"findings":[],"extra":1`, 1), "extra"},
		"empty":               {"", "review result"},
		"a result for no job": {good, "does not exist"}, // well formed, but no such job exists
	} {
		f := filepath.Join(t.TempDir(), "r.json")
		os.WriteFile(f, []byte(c.body), 0o600)
		rc, out, errOut := e.sub("review-record", "--result", f)
		if rc == exitcode.OK || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: rc %d stdout %q stderr %q, want it to say %q", name, rc, out, errOut, c.want)
		}
	}
	big := filepath.Join(t.TempDir(), "big.json")
	os.WriteFile(big, []byte(strings.Repeat(" ", (1<<20)+1)), 0o600)
	if rc, _, errOut := e.sub("review-record", "--result", big); rc == exitcode.OK || !strings.Contains(errOut, "1 MiB") {
		t.Errorf("a result over 1 MiB: rc %d %q", rc, errOut)
	}
	// Usage errors and a relay with no durable run.
	if rc, _, _ := e.run("build", "review-record", "--root", e.root, "--state-home", e.home, "--nope"); rc != exitcode.Usage {
		t.Errorf("an unknown flag: rc %d", rc)
	}
	legacy := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	legacy.run("build", "start", "--plan", legacy.plan, "--root", legacy.root)
	if rc, _, errOut := legacy.sub("review-record"); rc == exitcode.OK || !strings.Contains(errOut, "durable") {
		t.Errorf("review-record on a legacy relay: rc %d %q", rc, errOut)
	}
}

func TestRecoverRefusesAChangedPlanWithTheManualGateCode(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, started, _ := e.start()
	run := started["run_id"].(string)
	e.sub("tick")
	sent := e.tr.count()

	os.WriteFile(e.plan, []byte(cliPlan+"\n### Task 3: Third\n\nthird\n"), 0o644)
	for _, args := range [][]string{{"recover"}, {"start", "--plan", e.plan, "--resume-only", "--builder-kind", "codex"}} {
		rc, out, errOut := e.sub(args[0], args[1:]...)
		if rc != exitcode.ManualGate {
			t.Fatalf("%v: rc %d, want the manual gate; stderr %q", args, rc, errOut)
		}
		got := oneJSON(t, out)
		if got["run_id"] != run || got["state"] != "paused" || got["pause_code"] != "plan_changed" {
			t.Errorf("%v: %v", args, got)
		}
		if !strings.Contains(errOut, "reconcil") {
			t.Errorf("%v: stderr %q", args, errOut)
		}
	}
	if rc, out, _ := e.sub("tick"); rc != exitcode.OK || oneJSON(t, out)["state"] != "paused" {
		t.Errorf("tick after the refusal: rc %d %q", rc, out)
	}
	if e.tr.count() != sent {
		t.Errorf("%d prompts were sent after the plan changed", e.tr.count()-sent)
	}
	// The status of a paused run still reads back with exit 0.
	if rc, out, _ := e.sub("status", "--json"); rc != exitcode.OK || oneJSON(t, out)["pause_code"] != "plan_changed" {
		t.Errorf("status: rc %d %q", rc, out)
	}
}

// run3 runs a command and decodes its stdout, which must be one JSON object or empty.
func (e *cliEnv) run3(args ...string) (int, map[string]any, string, string) {
	e.t.Helper()
	rc, out, errOut := e.run(args...)
	var got map[string]any
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			e.t.Fatalf("stdout is not one JSON object: %q", out)
		}
	}
	return rc, got, out, errOut
}

func TestShareContextIsRememberedByTheRun(t *testing.T) {
	for _, shared := range []bool{false, true} {
		e := newCLI(t)
		var seen []transportSpec
		durableTransport = func(spec transportSpec) (runner.Transport, error) { seen = append(seen, spec); return e.tr, nil }
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		extra := []string{}
		if shared {
			extra = append(extra, "--share-context")
		}
		if rc, _, errOut := e.start(extra...); rc != exitcode.OK {
			t.Fatalf("start: %d %s", rc, errOut)
		}
		// Later commands are new processes: what the run was started with is read from the run.
		seen = nil
		e.sub("tick")
		e.sub("recover")
		if len(seen) != 2 {
			t.Fatalf("%d transports built", len(seen))
		}
		for _, spec := range seen {
			if spec.ShareContext != shared || spec.Kind != "codex" || spec.Builder != "builder" {
				t.Errorf("shared=%v: a later command built %+v", shared, spec)
			}
		}
	}
}

// A run paused because its task could not be delivered says why. The
// reason is the last failed send's error, which for a turn that ended at once
// holds what the pane showed.
func TestStatusOfARunPausedForDispatchSaysWhy(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	e.tr.notSent = errors.New("builder's turn ended as soon as it started. Its pane shows:\nThere's an issue with the selected model (sonnet-5.5).")
	e.start()
	for i := 0; i < 12; i++ {
		_, out, _ := e.sub("tick")
		if strings.Contains(out, "dispatch_failed") {
			break
		}
		time.Sleep(500 * time.Millisecond) // the retry backoff is 1s, then 2s
	}
	rc, out, _ := e.sub("status", "--json")
	got := oneJSON(t, out)
	if rc != exitcode.OK || got["pause_code"] != "dispatch_failed" {
		t.Fatalf("status: rc %d %v", rc, got)
	}
	if r, _ := got["pause_reason"].(string); !strings.Contains(r, "issue with the selected model") {
		t.Errorf("pause_reason %q", r)
	}
	_, text, _ := e.sub("status")
	if !strings.Contains(text, "reason: builder's turn ended as soon as it started") || !strings.Contains(text, "issue with the selected model") {
		t.Errorf("text status:\n%s", text)
	}
}

// Each attempt reports how its task prompt got on: tandem-handoff waits for
// the first one to be sent, or to fail, before it says the run started.
func TestStatusReportsEachAttemptsDispatch(t *testing.T) {
	dispatch := func(e *cliEnv) string {
		t.Helper()
		_, out, _ := e.sub("status", "--json")
		var got statusDurableOut
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Tasks) == 0 || len(got.Tasks[0].Attempts) == 0 {
			t.Fatalf("status: %v %q", err, out)
		}
		return got.Tasks[0].Attempts[0].Dispatch
	}
	sent := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	sent.start()
	sent.sub("tick")
	if got := dispatch(sent); got != "sent" {
		t.Errorf("a delivered task prompt: dispatch %q, want sent", got)
	}

	failed := newCLI(t)
	failed.tr.notSent = errors.New("the turn ended as soon as it started")
	failed.start()
	failed.sub("tick")
	if got := dispatch(failed); got != "pending" {
		t.Errorf("after one failed attempt: dispatch %q, want pending (it is retried)", got)
	}
	for i := 0; i < 12 && dispatch(failed) != "failed"; i++ {
		time.Sleep(500 * time.Millisecond)
		failed.sub("tick")
	}
	if got := dispatch(failed); got != "failed" {
		t.Errorf("after the last attempt: dispatch %q, want failed", got)
	}
}
