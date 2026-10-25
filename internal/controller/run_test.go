package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const twoTaskPlan = "# Greeting\n\n**Goal:** greet.\n\n### Task 1: First thing\n\nDo the first thing.\n\n### Task 2: Second thing\n\nDo the second thing.\n"

// fakeTransport stands in for Herdr and the agent CLIs.
type fakeTransport struct {
	mu    sync.Mutex
	sends []runner.Delivery
	obs   map[string]runner.Observation
	def   runner.Observation
	// sendErr, when set, decides a send's outcome before it is recorded.
	sendErr  func(runner.Delivery) error
	attempts int
	// noSession makes receipts report no session, as a transport that cannot
	// tell which conversation it reached.
	noSession bool
	// agents is what each named agent is observed doing, for prompts that go
	// to an agent other than the builder.
	agents map[string]runner.Observation
	// panes is what each named agent's pane shows.
	panes map[string]string
}

// ReadAgent is what the named agent's pane shows.
func (f *fakeTransport) ReadAgent(_ context.Context, name string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panes[name], nil
}

func (f *fakeTransport) setPane(name, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panes == nil {
		f.panes = map[string]string{}
	}
	f.panes[name] = text
}

// ObserveAgent observes an agent by name; one nobody set is unknown.
func (f *fakeTransport) ObserveAgent(_ context.Context, name string) (runner.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.agents[name]; ok {
		return o, nil
	}
	return runner.Observation{State: runner.ObsUnknown}, nil
}

func (f *fakeTransport) setAgent(name string, o runner.Observation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agents == nil {
		f.agents = map[string]runner.Observation{}
	}
	f.agents[name] = o
}

func (f *fakeTransport) Send(_ context.Context, d runner.Delivery) (runner.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.sendErr != nil {
		if err := f.sendErr(d); err != nil {
			return runner.Receipt{}, err
		}
	}
	f.sends = append(f.sends, d)
	if f.noSession {
		return runner.Receipt{EffectID: d.EffectID}, nil
	}
	return runner.Receipt{EffectID: d.EffectID, ExternalID: "session-" + d.EffectID[:6]}, nil
}

func (f *fakeTransport) Observe(_ context.Context, id string) (runner.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.obs[id]; ok {
		return o, nil
	}
	return f.def, nil
}

func (f *fakeTransport) set(id string, o runner.Observation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.obs == nil {
		f.obs = map[string]runner.Observation{}
	}
	f.obs[id] = o
}

// fakeChecker returns a check outcome without running a process. Everything it
// returns is stored in the real artifact store, as the real executor does.
type fakeChecker struct {
	art     *artifact.Store
	exit    int
	mutated bool
	calls   int
}

func (f *fakeChecker) run(ctx context.Context, req CheckRequest) (CheckResult, error) {
	f.calls++
	put := func(s string) string {
		h, err := f.art.Put(ctx, strings.NewReader(s))
		if err != nil {
			panic(err)
		}
		return h
	}
	res := CheckResult{JobID: req.JobID, AttemptID: req.AttemptID, Exit: f.exit, Complete: true, Summary: "fake check",
		BeforeInputsHash: put("inputs before"), CommandHash: put("command " + req.Command), EnvironmentHash: put("environment"), LogHash: put("log of " + req.JobID)}
	res.AfterInputsHash = res.BeforeInputsHash
	if f.mutated {
		res.AfterInputsHash = put("inputs after the check edited the tree")
	}
	return res, nil
}

type clock struct {
	mu sync.Mutex
	ms int64
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return time.UnixMilli(c.ms) }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.ms += d.Milliseconds()
	c.mu.Unlock()
}

type cenv struct {
	t     *testing.T
	st    *state.Store
	art   *artifact.Store
	c     *Controller
	root  string
	relay string
	plan  string
	tr    *fakeTransport
	chk   *fakeChecker
	clk   *clock
	ids   int
	sendN int
	runID string
	// stRoot is the state root: the database and the artifact store live in it.
	stRoot string

	// Direct review, off unless a test turns it on before starting a run.
	reviewer     runner.Reviewer
	reviewLaunch func(kind string) ([]string, error)
	// reviewerKind is the kind of the reviewer pane e.start gives a run; claude when empty.
	reviewerKind string
	// builderKind is the kind of the builder e.start gives a run; codex when empty.
	builderKind   string
	reviewTimeout time.Duration
	// usage stands in for the agents' logs; nil means none are readable.
	usage UsageSource
	// logs stands in for the provider logs collaboration collection reads.
	logs SessionLogs
	// owner, when set, names every controller this env builds, so a "restarted"
	// controller is the same logical writer.
	owner string
	// spent is what each session's fake log has recorded so far.
	spent map[string]int64
	lines map[string]int
}

func newCEnv(t *testing.T) *cenv {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(root, 0o755)
	git(t, root, "init", "-q")
	write(t, root, ".gitignore", ".tandem-relay/\n")
	write(t, root, "README", "readme\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "init")
	plan := filepath.Join(root, "plan.md")
	os.WriteFile(plan, []byte(twoTaskPlan), 0o644)
	// The plan sits in the tree, untracked; commit it so it is not task work.
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "plan")

	// A state root is named by its checkout id, which is 32 hex characters.
	stRoot := filepath.Join(t.TempDir(), strings.Repeat("c", 32))
	st, err := state.Open(ctx, stRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	art, err := artifact.Open(stRoot)
	if err != nil {
		t.Fatal(err)
	}
	// The registry names the checkout's state root by its id; this test store
	// is a root of its own, so the checkout id is its directory name.
	e := &cenv{t: t, st: st, art: art, stRoot: stRoot, root: root, relay: paths.Relay(root), plan: plan, tr: &fakeTransport{}, chk: &fakeChecker{art: art}, clk: &clock{ms: 10_000_000}}
	e.c = e.newController()
	return e
}

// artRoot is the artifact store's directory.
func (e *cenv) artRoot() string { return filepath.Join(e.stRoot, "artifacts") }

func (e *cenv) newController() *Controller {
	e.t.Helper()
	c, err := New(Dependencies{State: e.st, Artifacts: e.art, Transport: e.tr, CheckExecutor: e.chk.run, Now: e.clk.now, NewID: e.newID,
		Reviewer: e.reviewer, ReviewLaunch: e.reviewLaunch, ReviewTimeout: e.reviewTimeout, UsageSource: e.usage, SessionLogs: e.logs, Owner: e.owner})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *cenv) newID() (string, error) {
	e.ids++
	return fmt.Sprintf("%032x", 0xA000+e.ids), nil
}

func (e *cenv) start() string {
	e.t.Helper()
	id, err := e.c.Start(ctx, e.startRequest())
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.runID = id
	return id
}

func (e *cenv) tick() {
	e.t.Helper()
	if err := e.c.Tick(ctx, e.runID); err != nil {
		e.t.Fatalf("Tick: %v", err)
	}
}

func (e *cenv) tasks() []state.TaskRow {
	e.t.Helper()
	ts, err := e.st.RunTasks(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	return ts
}

func (e *cenv) current(taskID string) workflow.Attempt {
	e.t.Helper()
	as, err := e.st.TaskAttempts(ctx, e.runID, taskID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, a := range as {
		if a.Current {
			return a
		}
	}
	e.t.Fatalf("task %s has no current attempt among %d", taskID, len(as))
	return workflow.Attempt{}
}

func (e *cenv) effects(kind string) []workflow.Effect {
	e.t.Helper()
	all, err := e.st.RunEffects(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []workflow.Effect
	for _, x := range all {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

// delivered counts sends by the kind of the effect they carried.
func (e *cenv) delivered() map[string]int {
	e.t.Helper()
	e.tr.mu.Lock()
	defer e.tr.mu.Unlock()
	out := map[string]int{}
	for _, d := range e.tr.sends {
		eff, err := e.st.GetEffect(ctx, d.EffectID)
		if err != nil {
			e.t.Fatal(err)
		}
		out[eff.Kind]++
	}
	return out
}

func (e *cenv) runState() (string, string) {
	e.t.Helper()
	r, err := e.st.GetRun(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.State, r.PauseCode
}

func (e *cenv) builderEffect(att workflow.Attempt) workflow.Effect {
	e.t.Helper()
	for _, x := range e.effects(workflow.EffectBuilderDispatch) {
		if x.AttemptID == att.ID {
			return x
		}
	}
	e.t.Fatalf("attempt %s has no builder dispatch", att.ID)
	return workflow.Effect{}
}

// send plays the builder: it writes a payload and an envelope for kind, bound
// to the attempt's dispatch effect and nonce, and ingests it.
func (e *cenv) send(taskID, kind, payload string) error {
	e.t.Helper()
	att := e.current(taskID)
	eff := e.builderEffect(att)
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "builder")
	if err != nil || len(jobs) != 1 {
		e.t.Fatalf("builder jobs %v %v", jobs, err)
	}
	e.sendN++
	name := fmt.Sprintf("payloads/%s-%d.json", kind, e.sendN)
	dir := RunDir(e.relay, e.runID)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
		e.t.Fatal(err)
	}
	env := Envelope{SchemaVersion: 2, EventID: fmt.Sprintf("%032x", 0xB000+e.sendN), RunID: e.runID, TaskID: taskID, AttemptID: att.ID,
		JobID: jobs[0].ID, EffectID: eff.ID, Nonce: eff.Nonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)}
	raw, _ := json.Marshal(env)
	_, err = e.c.Ingest(ctx, raw)
	return err
}

func (e *cenv) mustSend(taskID, kind, payload string) {
	e.t.Helper()
	if err := e.send(taskID, kind, payload); err != nil {
		e.t.Fatalf("%s: %v", kind, err)
	}
}

// settle reports the builder's turn finished, as its transport would.
func (e *cenv) settle(att workflow.Attempt) {
	eff := e.builderEffect(att)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsCompleted, Nonce: eff.Nonce})
}

// review plays the reviewer for the attempt's open review job.
func (e *cenv) review(att workflow.Attempt, status string, findings ...workflow.Finding) error {
	e.t.Helper()
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if err != nil || len(jobs) == 0 {
		e.t.Fatalf("reviewer jobs %v %v", jobs, err)
	}
	job := jobs[len(jobs)-1]
	res := workflow.ReviewResult{SchemaVersion: 1, JobID: job.ID, AttemptID: att.ID, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash, Status: status, Findings: findings}
	if res.Findings == nil {
		res.Findings = []workflow.Finding{}
	}
	e.reviewerFinished(att, "reviewer-session")
	_, err = e.c.RecordReview(ctx, e.runID, res)
	return err
}

// reviewerFinished reports the pane that was asked to review as settled, as its
// transport would, with the session it observed (none when session is "").
func (e *cenv) reviewerFinished(att workflow.Attempt, session string) {
	e.t.Helper()
	e.reviewerObserved(att, runner.Observation{State: runner.ObsCompleted, ExternalID: session})
}

func (e *cenv) reviewerObserved(att workflow.Attempt, obs runner.Observation) {
	e.t.Helper()
	for _, x := range e.effects("review_prompt") {
		if x.AttemptID == att.ID {
			e.tr.set(x.ID, obs)
			e.tr.setAgent("reviewer-session", obs)
			return
		}
	}
	e.t.Fatalf("attempt %s has no review prompt", att.ID)
}

// finishTask drives one task from its first tick to an accepted review.
func (e *cenv) work(n int) {
	e.t.Helper()
	write(e.t, e.root, fmt.Sprintf("work%d.txt", n), fmt.Sprintf("work for task %d\n", n))
}

func (e *cenv) leaseOwned() bool {
	e.t.Helper()
	run, _ := e.st.GetRun(ctx, e.runID)
	l, _ := e.st.LeaseState(ctx, run.CheckoutID)
	return l.State == "owned"
}

func TestDurableTwoTaskLifecycle(t *testing.T) {
	e := newCEnv(t)
	run := e.start()
	if ar, err := ReadActiveRun(e.relay); err != nil || ar == nil || ar.RunID != run {
		t.Fatalf("marker %+v %v", ar, err)
	}
	if _, err := os.Stat(filepath.Join(e.relay, "build", "build.json")); !os.IsNotExist(err) {
		t.Fatal("a durable run left a legacy build record")
	}
	if got := e.tasks(); len(got) != 2 {
		t.Fatalf("tasks %+v", got)
	}

	for n := 1; n <= 2; n++ {
		task := e.tasks()[n-1]
		e.tick() // delivers the builder's task prompt (already done for task 2 by the previous acceptance)
		if got := e.delivered()[workflow.EffectBuilderDispatch]; got != n {
			t.Fatalf("task %d: %d builder prompts sent", n, got)
		}
		att := e.current(task.ID)
		eff := e.builderEffect(att)

		e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
		if got, _ := e.st.GetEffect(ctx, eff.ID); got.State != "acknowledged" {
			t.Fatalf("the start acknowledgement left the prompt %s", got.State)
		}
		if cur := e.current(task.ID); cur.Implementation != workflow.ImplementationRunning {
			t.Fatalf("implementation %s", cur.Implementation)
		}

		e.work(n)
		e.settle(att)
		e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
		sub := e.current(task.ID)
		if sub.Implementation != workflow.ImplementationSubmitted || sub.EndSnapshotHash == "" || sub.PatchHash == "" {
			t.Fatalf("submitted attempt %+v: the controller must have captured the end snapshot and patch", sub)
		}

		e.tick() // runs the check, then sends the review prompt
		if e.chk.calls != n {
			t.Fatalf("%d checks", e.chk.calls)
		}
		afterCheck := e.current(task.ID)
		if afterCheck.Check != workflow.CheckPassed || afterCheck.Review != workflow.ReviewPending {
			t.Fatalf("check %s review %s", afterCheck.Check, afterCheck.Review)
		}
		if got := e.delivered()["review_prompt"]; got != n {
			t.Fatalf("%d review prompts", got)
		}
		e.tick() // nothing may be accepted while the review is pending
		if e.tasks()[n-1].AcceptedAttemptID != "" {
			t.Fatal("a task was accepted before its review")
		}

		if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
			t.Fatal(err)
		}
		e.tick() // accepts, then sends the next task or the final notice
		if e.tasks()[n-1].AcceptedAttemptID != att.ID {
			t.Fatalf("task %d was not accepted", n)
		}
	}

	got := e.delivered()
	if got["notify_planner"] != 1 || got[workflow.EffectBuilderDispatch] != 2 || got["review_prompt"] != 2 {
		t.Fatalf("deliveries %v", got)
	}
	if state, code := e.runState(); state != "finished" || code != "" {
		t.Fatalf("run %s/%s", state, code)
	}
	if e.leaseOwned() {
		t.Fatal("the writer lease was kept after the run finished")
	}
	before := len(e.tr.sends)
	e.tick()
	e.tick()
	if len(e.tr.sends) != before {
		t.Fatal("a finished run kept sending")
	}
	// The final notice is recorded as an effect and was acknowledged once.
	if n := e.effects("notify_planner"); len(n) != 1 || n[0].State != "acknowledged" {
		t.Fatalf("notice %+v", n)
	}
}

func TestRecoverPendingNextDispatch(t *testing.T) {
	e := newCEnv(t)
	e.start()
	first := e.tasks()[0]
	e.tick()
	att := e.current(first.ID)
	e.mustSend(first.ID, workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend(first.ID, workflow.EventBuilderSubmitted, "{}\n")
	e.tick()
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}

	// Crash immediately after the acceptance commits.
	crash := errors.New("process died after the acceptance commit")
	e.c.hooks.afterAccept = func() error { return crash }
	if err := e.c.Tick(ctx, e.runID); !errors.Is(err, crash) {
		t.Fatalf("err = %v", err)
	}
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Fatal("the acceptance did not commit")
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, e.tasks()[1].ID); len(as) != 0 {
		t.Fatal("the next task started before the crash")
	}
	if pending := e.effects(workflow.EffectAdvanceRun); len(pending) != 1 || pending[0].State != "pending" {
		t.Fatalf("the acceptance must leave its next-step effect durable: %+v", pending)
	}

	// A new process, after the old writer's lease has run out.
	e.clk.advance(11 * time.Minute)
	c2 := e.newController()
	if err := c2.Recover(ctx, e.runID); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 2 {
		t.Fatalf("%d builder prompts after recovery, want task 2's once", got)
	}
	old := e.c
	e.c = c2
	e.tick()
	e.tick()
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 2 {
		t.Fatalf("the next dispatch was repeated: %d", got)
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, e.tasks()[1].ID); len(as) != 1 {
		t.Fatalf("%d attempts for task 2", len(as))
	}
	if n := len(e.effects(workflow.EffectBuilderDispatch)); n != 2 {
		t.Fatalf("%d builder dispatch effects", n)
	}
	// The old process is fenced out: it can neither tick nor send.
	before := len(e.tr.sends)
	if err := old.Tick(ctx, e.runID); !errors.Is(err, ErrNotWriter) {
		t.Fatalf("the old writer ticked after it was replaced: %v", err)
	}
	if len(e.tr.sends) != before {
		t.Fatal("the old writer sent something after it was replaced")
	}
}

func TestStopWaitsForOwnedWriter(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.tick()
	att := e.current(task.ID)
	eff := e.builderEffect(att)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce}) // the builder is still working

	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if state, _ := e.runState(); state != "stopping" {
		t.Fatalf("run %s", state)
	}
	e.tick()
	if got := e.delivered()["cancel"]; got != 1 {
		t.Fatalf("%d cancel messages", got)
	}
	if state, _ := e.runState(); state != "stopping" || !e.leaseOwned() {
		t.Fatalf("a running builder must keep the run stopping and the lease held: state %s lease %v", state, e.leaseOwned())
	}
	e.tick()
	if got := e.delivered()["cancel"]; got != 1 {
		t.Fatalf("the cancel message was repeated: %d", got)
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, task.ID); len(as) != 1 {
		t.Fatal("new work started while stopping")
	}

	e.tr.set(eff.ID, runner.Observation{State: runner.ObsStopped, Nonce: eff.Nonce})
	e.tick()
	if state, code := e.runState(); state != "paused" || code != "stopped" {
		t.Fatalf("after termination: %s/%s", state, code)
	}
	if e.leaseOwned() {
		t.Fatal("the lease was kept after the writer terminated")
	}
	// Stopping twice changes nothing.
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatalf("a second stop: %v", err)
	}
}

func TestStopEndsTheReviewerJobOfAPromptThatWasNeverSent(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	// The review prompt cannot be sent, so it stays pending beside its job.
	e.tr.sendErr = func(d runner.Delivery) error { return runner.NotSent(errors.New("pane busy")) }
	e.tick()
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(jobs) != 1 || jobs[0].State == "cancelled" {
		t.Fatalf("setup: %+v", jobs)
	}
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	jobs, _ = e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(jobs) != 1 || jobs[0].State != "cancelled" {
		t.Fatalf("the reviewer job of a review that was never requested is %+v", jobs)
	}
}

func TestStopCancelsWorkThatNeverStarted(t *testing.T) {
	e := newCEnv(t)
	e.start()
	if err := e.c.Stop(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.delivered(); got[workflow.EffectBuilderDispatch] != 0 {
		t.Fatalf("a prompt was sent to a run that was told to stop: %v", got)
	}
	if n := e.effects(workflow.EffectBuilderDispatch); len(n) != 1 || n[0].State != "cancelled" {
		t.Fatalf("%+v", n)
	}
	// The job that prompt was for never ran, and is not left open.
	jobs, err := e.st.AttemptJobs(ctx, e.current("task-1").ID, "builder")
	if err != nil || len(jobs) != 1 || jobs[0].State != "cancelled" {
		t.Fatalf("the builder job of a prompt that was never sent is %+v %v", jobs, err)
	}
	e.tick()
	if state, code := e.runState(); state != "paused" || code != "stopped" || e.leaseOwned() {
		t.Fatalf("%s/%s lease %v: with nothing running the stop completes", state, code, e.leaseOwned())
	}
}

func TestUnknownWriterBlocksNewTask(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick() // the builder prompt is sent and never acknowledged
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("%d prompts", got)
	}
	e.clk.advance(state.AckWindow + time.Minute)
	e.tick() // the acknowledgement window passes; the transport cannot tell what happened

	if state, code := e.runState(); state != "paused" || code != "writer_unknown" {
		t.Fatalf("run %s/%s", state, code)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("an uncertain prompt was sent again: %d", got)
	}
	eff := e.effects(workflow.EffectBuilderDispatch)[0]
	if eff.State != "delivery_unknown" {
		t.Fatalf("effect %s", eff.State)
	}
	// No second writer: not another run, not another controller.
	other := e.newController()
	if _, err := other.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderSession, r.PlannerSession = "b", "p" })); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("a second run on a checkout with an unknown writer: %v", err)
	}
	for i := 0; i < 3; i++ {
		e.tick()
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, e.tasks()[1].ID); len(as) != 0 {
		t.Fatal("a second task started beside the unknown writer")
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("%d prompts", got)
	}
	// If the builder later proves it read the prompt, the effect resolves.
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
	e.clk.advance(2 * time.Minute)
	if err := e.c.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.GetEffect(ctx, eff.ID); got.State != "acknowledged" {
		t.Fatalf("after the nonce was seen: %s", got.State)
	}
}

func TestEngineSelectionPersistsAcrossEnvChange(t *testing.T) {
	relay := filepath.Join(t.TempDir(), ".tandem-relay")
	os.MkdirAll(relay, 0o755)
	choose := func(env string) (Engine, error) { return SelectEngine(relay, env) }

	if got, err := choose(""); err != nil || got != EngineLegacy {
		t.Fatalf("a fresh relay with the variable unset: %v %v", got, err)
	}
	if got, err := choose("durable"); err != nil || got != EngineDurable {
		t.Fatalf("a fresh relay opting in: %v %v", got, err)
	}
	if got, err := choose("legacy"); err != nil || got != EngineLegacy {
		t.Fatalf("explicitly legacy: %v %v", got, err)
	}
	if _, err := choose("sometimes"); err == nil {
		t.Fatal("an unknown engine name was accepted")
	}

	// A durable run is marked; from then on the variable is irrelevant.
	if _, err := InitRunDir(relay, ActiveRun{RunID: strings.Repeat("a", 32), ProjectID: strings.Repeat("b", 32)},
		RunManifest{RunID: strings.Repeat("a", 32), Tasks: []ManifestTask{{Ordinal: 1, ID: "task-1"}}}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"", "legacy", "durable", "garbage"} {
		if got, err := choose(env); err != nil || got != EngineDurable {
			t.Errorf("a durable run with TANDEM_BUILD_ENGINE=%q: %v %v", env, got, err)
		}
	}

	// A legacy build keeps its engine even if the variable is later turned on.
	legacy := filepath.Join(t.TempDir(), ".tandem-relay")
	os.MkdirAll(filepath.Join(legacy, "build"), 0o755)
	os.WriteFile(filepath.Join(legacy, "build", "build.json"), []byte("{}"), 0o644)
	for _, env := range []string{"", "durable"} {
		if got, err := SelectEngine(legacy, env); err != nil || got != EngineLegacy {
			t.Errorf("a legacy build with TANDEM_BUILD_ENGINE=%q: %v %v", env, got, err)
		}
	}
	// Both at once is a mixed run: refused, never guessed.
	os.WriteFile(filepath.Join(relay, "x"), nil, 0o644)
	os.MkdirAll(filepath.Join(relay, "build"), 0o755)
	os.WriteFile(filepath.Join(relay, "build", "build.json"), []byte("{}"), 0o644)
	if _, err := choose("durable"); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("a relay with both engines: %v", err)
	}
	// A marker that cannot be read stops the choice.
	os.WriteFile(filepath.Join(relay, ActiveRunFile), []byte("{broken"), 0o644)
	if _, err := choose("legacy"); err == nil {
		t.Fatal("a corrupt marker fell back to another engine")
	}
}

func TestTickRequiresTheWriterLease(t *testing.T) {
	e := newCEnv(t)
	e.start()
	other := e.newController()
	if err := other.Tick(ctx, e.runID); !errors.Is(err, ErrNotWriter) {
		t.Fatalf("a controller that never acquired the lease ticked: %v", err)
	}
	if err := other.Stop(ctx, e.runID); err != nil {
		t.Fatalf("Stop records an intent and needs no lease: %v", err)
	}
}

func TestCheckFailureAndMutationPauseTheRun(t *testing.T) {
	for name, tc := range map[string]struct {
		exit    int
		mutated bool
		code    string
	}{
		"a failing check":    {exit: 1, code: "check_failed"},
		"a check that edits": {mutated: true, code: "source_mutated"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			e.start()
			e.chk.exit, e.chk.mutated = tc.exit, tc.mutated
			task := e.tasks()[0]
			e.tick()
			att := e.current(task.ID)
			e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
			e.work(1)
			e.settle(att)
			e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{}\n")
			e.tick()
			if state, code := e.runState(); state != "paused" || code != tc.code {
				t.Fatalf("run %s/%s, want paused/%s", state, code, tc.code)
			}
			if got := e.delivered()["review_prompt"]; got != 0 {
				t.Fatal("a review was requested for a check that did not pass")
			}
			if e.tasks()[0].AcceptedAttemptID != "" {
				t.Fatal("accepted")
			}
			calls := e.chk.calls
			e.tick()
			if e.chk.calls != calls {
				t.Fatal("a paused run ran the check again; checks must not loop")
			}
		})
	}
}

func TestAcceptanceNeedsAReviewOrAWaiverForThisTree(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.tick()
	att := e.current(task.ID)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{}\n")
	e.tick()
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Fatal("accepted with a green check and no review")
	}
	if err := e.c.Waive(ctx, e.runID, task.ID, "review", "", "planner"); err == nil {
		t.Fatal("a waiver with no reason")
	}
	if err := e.c.Waive(ctx, e.runID, task.ID, "review", "I read the diff and the report", "planner"); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Fatal("a recorded review waiver for this tree did not allow acceptance")
	}
	if cur, _ := e.st.GetAttempt(ctx, att.ID); cur.Review != workflow.ReviewWaived {
		t.Fatalf("review state %s: a waiver must not read as a passed review", cur.Review)
	}
}

func TestRepairCreatesALinkedAttemptAndStopsAfterThree(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	finding := workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "wrong"}
	for n := 1; n <= 3; n++ {
		e.tick()
		att := e.current(task.ID)
		if att.Ordinal != n {
			t.Fatalf("attempt ordinal %d, want %d", att.Ordinal, n)
		}
		if n > 1 && att.ParentAttemptID == "" {
			t.Fatal("a repair attempt is not linked to its parent")
		}
		e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
		write(t, e.root, "work1.txt", fmt.Sprintf("attempt %d\n", n))
		e.settle(att)
		e.mustSend(task.ID, workflow.EventBuilderSubmitted, "{}\n")
		e.tick()
		if err := e.review(att, workflow.ReviewStatusChangesRequested, finding); err != nil {
			t.Fatal(err)
		}
		e.tick()
	}
	if state, code := e.runState(); state != "paused" || code != "repairs_exhausted" {
		t.Fatalf("after three attempts: %s/%s", state, code)
	}
	as, _ := e.st.TaskAttempts(ctx, e.runID, task.ID)
	if len(as) != 3 {
		t.Fatalf("%d attempts", len(as))
	}
	if e.tasks()[0].AcceptedAttemptID != "" {
		t.Fatal("accepted")
	}
}

func TestBuilderStuckPausesTheRun(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.tick()
	e.mustSend(task.ID, workflow.EventBuilderStuck, "{\"why\":\"cannot\"}\n")
	if state, code := e.runState(); state != "paused" || code != workflow.PauseStuck {
		t.Fatalf("%s/%s", state, code)
	}
}

func TestStartRefusesAnIncompleteSnapshotAndCreatesNothing(t *testing.T) {
	e := newCEnv(t)
	nested := filepath.Join(e.root, "nested")
	os.MkdirAll(nested, 0o755)
	git(t, nested, "init", "-q")
	write(t, nested, "x", "x")
	_, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderSession, r.PlannerSession = "b", "p" }))
	if !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("err = %v", err)
	}
	if ar, _ := ReadActiveRun(e.relay); ar != nil {
		t.Fatal("a failed start left a marker")
	}
	if got, _, _ := e.st.Report(ctx, e.st.CheckoutID()); got != nil {
		t.Fatalf("a failed start left a run: %+v", got)
	}
}

func TestStartTwiceOnOneCheckoutIsBusy(t *testing.T) {
	e := newCEnv(t)
	e.start()
	_, err := e.newController().Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderSession, r.PlannerSession = "b", "p" }))
	if !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.PlanPath, r.BuilderSession, r.PlannerSession, r.CheckCommand = filepath.Join(e.root, "nope.md"), "", "", ""
	})); err == nil {
		t.Fatal("a plan that does not exist started")
	}
}

func TestTickIngestsQueuedMailboxMessages(t *testing.T) {
	e := newCEnv(t)
	e.start()
	task := e.tasks()[0]
	e.tick()
	att := e.current(task.ID)
	eff := e.builderEffect(att)
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "builder")
	dir := RunDir(e.relay, e.runID)
	os.WriteFile(filepath.Join(dir, "payloads", "s.json"), []byte("{}\n"), 0o644)
	env := Envelope{SchemaVersion: 2, EventID: fmt.Sprintf("%032x", 0xC001), RunID: e.runID, TaskID: task.ID, AttemptID: att.ID, JobID: jobs[0].ID,
		EffectID: eff.ID, Nonce: eff.Nonce, Kind: workflow.EventBuilderStarted, PayloadPath: "payloads/s.json", PayloadHash: sha("{}\n")}
	raw, _ := json.Marshal(env)
	queued := filepath.Join(dir, "q", env.EventID+".json")
	os.WriteFile(queued, raw, 0o644)
	forged := filepath.Join(dir, "q", "forged.json")
	os.WriteFile(forged, []byte(`{"schema_version":2,"kind":"accept_requested"}`), 0o644)

	e.tick()
	if got, _ := e.st.GetEffect(ctx, eff.ID); got.State != "acknowledged" {
		t.Fatalf("the queued start was not ingested: %s", got.State)
	}
	if _, err := os.Stat(queued); !os.IsNotExist(err) {
		t.Fatal("an ingested message stayed queued")
	}
	if _, err := os.Stat(filepath.Join(dir, "receipts", env.EventID+".json")); err != nil {
		t.Fatalf("no receipt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "quarantine", "forged.json.taken")); err != nil {
		t.Fatalf("the forged message was not quarantined: %v", err)
	}
}

func TestPackageDoesNotUseGlobalMutableDependencies(t *testing.T) {
	// Two controllers over separate stores must not share anything.
	a, b := newCEnv(t), newCEnv(t)
	ra, rb := a.start(), b.start()
	a.tick()
	if got := b.delivered(); len(got) != 0 {
		t.Fatalf("a tick of one controller reached the other's transport: %v", got)
	}
	b.tick()
	if ra == "" || rb == "" || len(a.tr.sends) != 1 || len(b.tr.sends) != 1 {
		t.Fatalf("%s %s %d %d", ra, rb, len(a.tr.sends), len(b.tr.sends))
	}
}

func TestRecoverWithAnUnknownWriterPausesAndTakesNothingOver(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick() // the prompt is sent; the old process then dies before any acknowledgement
	e.clk.advance(state.AckWindow + time.Minute)

	c2 := e.newController()
	if err := c2.Recover(ctx, e.runID); err != nil {
		t.Fatalf("Recover reports a paused run by state, not by error: %v", err)
	}
	if state, code := e.runState(); state != "paused" || code != "writer_unknown" {
		t.Fatalf("run %s/%s", state, code)
	}
	run, _ := e.st.GetRun(ctx, e.runID)
	lease, _ := e.st.LeaseState(ctx, run.CheckoutID)
	if lease.State != "reconciling" {
		t.Fatalf("the lease was %s: an unknown writer's lease must not be taken over", lease.State)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("%d prompts: recovery must not resend an unconfirmed one", got)
	}
	if err := c2.Tick(ctx, e.runID); err != nil {
		t.Fatalf("a paused run's tick is a no-op: %v", err)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("%d prompts", got)
	}
	// Once the builder shows its nonce, a later recovery proceeds.
	eff := e.effects(workflow.EffectBuilderDispatch)[0]
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
	e.clk.advance(time.Minute)
	if err := c2.Recover(ctx, e.runID); err != nil {
		t.Fatal(err)
	}
	if state, _ := e.runState(); state != "running" {
		t.Fatalf("after the writer was identified: %s", state)
	}
}

// queueClaimed plays the builder through the mailbox, as a sandboxed Codex
// builder does: it writes the payload and the envelope as a claimed queue file
// and returns its path, for whoever ingests the queue.
func (e *cenv) queueClaimed(taskID, kind, payload string) string {
	e.t.Helper()
	att := e.current(taskID)
	eff := e.builderEffect(att)
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "builder")
	if err != nil || len(jobs) != 1 {
		e.t.Fatalf("builder jobs %v %v", jobs, err)
	}
	e.sendN++
	name := fmt.Sprintf("payloads/%s-%d.json", kind, e.sendN)
	dir := RunDir(e.relay, e.runID)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
		e.t.Fatal(err)
	}
	env := Envelope{SchemaVersion: 2, EventID: fmt.Sprintf("%032x", 0xC000+e.sendN), RunID: e.runID, TaskID: taskID, AttemptID: att.ID,
		JobID: jobs[0].ID, EffectID: eff.ID, Nonce: eff.Nonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)}
	raw, _ := json.Marshal(env)
	claimed := filepath.Join(dir, "q", env.EventID+".json.taken")
	if err := os.WriteFile(claimed, raw, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return claimed
}

// A submission ingested outside a tick, as the relay bridge does, is applied
// as a tick applies it: the end snapshot and patch are captured first.
func TestAQueuedSubmissionIngestedOutsideATickCapturesItsEvidence(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	task := e.tasks()[0]
	att := e.current(task.ID)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	claimed := e.queueClaimed(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	res, err := e.c.IngestClaimed(ctx, e.runID, claimed)
	if err != nil || res.Disposition != Committed {
		t.Fatalf("ingest: %+v %v", res, err)
	}
	sub := e.current(task.ID)
	if sub.Implementation != workflow.ImplementationSubmitted || sub.EndSnapshotHash == "" || sub.PatchHash == "" {
		t.Fatalf("submitted attempt %+v", sub)
	}
}

// A builder hands back from inside its turn and may write more afterwards. A
// submission from a builder still working waits in the queue, untouched, and is
// applied once the turn ends.
func TestASubmissionSentMidTurnWaitsForTheTurnToEnd(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	task := e.tasks()[0]
	att := e.current(task.ID)
	e.mustSend(task.ID, workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	eff := e.builderEffect(att)
	e.tr.set(eff.ID, runner.Observation{State: runner.ObsRunning, Nonce: eff.Nonce})
	claimed := e.queueClaimed(task.ID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")

	res, err := e.c.IngestClaimed(ctx, e.runID, claimed)
	if err == nil || res.Disposition == Quarantined {
		t.Fatalf("a submission from a working builder: %+v %v; want it left for a retry", res, err)
	}
	if q, _ := filepath.Glob(filepath.Join(RunDir(e.relay, e.runID), "quarantine", "*")); len(q) != 0 {
		t.Fatalf("quarantined: %v", q)
	}
	if cur := e.current(task.ID); cur.Implementation != workflow.ImplementationRunning || cur.EndSnapshotHash != "" {
		t.Fatalf("the attempt moved, or its end was captured mid-turn: %+v", cur)
	}

	e.settle(att)
	e.tick()
	if cur := e.current(task.ID); cur.Implementation != workflow.ImplementationSubmitted || cur.PatchHash == "" {
		t.Fatalf("once the turn ended the submission was not applied: %+v", cur)
	}
}

func TestSelectEngineWhySaysWhatDecided(t *testing.T) {
	relay := t.TempDir()
	for _, tc := range []struct {
		env, repo string
		want      Engine
		why       string
	}{
		{"", "", EngineLegacy, "the default"},
		{"", "durable", EngineDurable, ".tandem.json"},
		{"legacy", "durable", EngineLegacy, "TANDEM_BUILD_ENGINE"},
		{"durable", "legacy", EngineDurable, "TANDEM_BUILD_ENGINE"},
	} {
		got, why, err := SelectEngineWhy(relay, tc.env, tc.repo)
		if err != nil || got != tc.want || why != tc.why {
			t.Errorf("env %q repo %q: got %s %q %v, want %s %q", tc.env, tc.repo, got, why, err, tc.want, tc.why)
		}
	}
	if _, _, err := SelectEngineWhy(relay, "", "newest"); err == nil {
		t.Error("an unknown repo engine was accepted")
	}
}

// startRequest is the StartRequest a test starts a run with: this env's plan,
// a builder, a planner, a reviewer pane and a passing check. Each mut sets
// what that test is about.
func (e *cenv) startRequest(mut ...func(*StartRequest)) StartRequest {
	req := StartRequest{Root: e.root, PlanPath: e.plan, BuilderKind: e.builderKindOrDefault(), BuilderSession: "builder-session",
		PlannerSession: "planner-session", CheckCommand: "true", ReviewerKind: e.reviewerKindOrDefault(), ReviewerSession: "reviewer-session"}
	for _, m := range mut {
		m(&req)
	}
	return req
}

func (e *cenv) reviewerKindOrDefault() string {
	if e.reviewerKind == "" {
		return "claude"
	}
	return e.reviewerKind
}

func (e *cenv) builderKindOrDefault() string {
	if e.builderKind == "" {
		return "codex"
	}
	return e.builderKind
}
