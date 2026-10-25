package controller

import (
	"fmt"
	"hash/crc32"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/evalrow"
	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// The evaluation drives the real controller: Start, Tick, Ingest, RecordReview
// and Recover. The agents are scripted, so they send the protocol events a
// real builder would. Nothing here edits run state.

const scenarioFile = "../../internal/evalrow/testdata/scenarios.json"

func loadScenarios(t *testing.T) evalrow.File {
	t.Helper()
	f, err := evalrow.Load(scenarioFile)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func scenario(t *testing.T, id string) evalrow.Scenario {
	t.Helper()
	for _, s := range loadScenarios(t).Scenarios {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no scenario %q", id)
	return evalrow.Scenario{}
}

// evalEnv is a controller environment for one scenario, with a scripted log for
// each builder session.
type evalEnv struct {
	*cenv
	src   *fakeUsage
	sc    evalrow.Scenario
	start time.Time
}

func newEvalEnv(t *testing.T, sc evalrow.Scenario) *evalEnv {
	t.Helper()
	e := newCEnv(t)
	src := &fakeUsage{streams: map[string][]rollout.Stream{}, unsupported: map[string]bool{}}
	e.usage = src
	e.owner = "eval-controller"
	e.c = e.newController()
	e.setPlan(sc.Plan())
	if sc.Dirty {
		write(t, e.root, "unrelated.tmp", "someone else's edit\n")
		write(t, e.root, "shared.txt", "base\n")
		git(t, e.root, "add", "shared.txt")
		git(t, e.root, "commit", "-qm", "shared")
	}
	return &evalEnv{cenv: e, src: src, sc: sc, start: e.clk.now()}
}

// step advances the fixture clock, so a run's latency is deterministic.
func (e *evalEnv) step() { e.clk.advance(time.Second) }

func (e *evalEnv) begin() error {
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ContextLimit = e.sc.ContextLimit }))
	e.runID = id
	return err
}

// logUsage files what the task's session spent. In a partial-usage scenario
// the second task's child log cannot be read.
func (e *evalEnv) logUsage(att workflow.Attempt, ordinal int) {
	s := e.sessionOf(att)
	streams := []rollout.Stream{stream(s, "", ev(s, 2, 1000, 700, 50))}
	if e.sc.PartialUsage && ordinal == 2 {
		streams = append(streams, rollout.Stream{Provider: "codex", SessionID: "child-" + s, ParentID: s, File: "child.jsonl", Unavailable: "line 3 does not decode"})
	}
	e.src.streams[s] = streams
}

// attempt plays one builder attempt: start, work, submit, check.
func (e *evalEnv) attempt(taskID string, n int) workflow.Attempt {
	e.t.Helper()
	e.tick()
	e.step()
	att := e.current(taskID)
	e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
	if e.sc.Dirty {
		write(e.t, e.root, "shared.txt", fmt.Sprintf("edited by %s attempt %d\n", taskID, att.Ordinal))
	}
	e.work(n)
	e.settle(att)
	e.mustSend(taskID, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	e.step()
	e.logUsage(att, n)
	return att
}

// playTask takes one task to acceptance, with the scenario's red reviews first.
func (e *evalEnv) playTask(taskID string, n int, red *int) {
	e.t.Helper()
	for {
		att := e.attempt(taskID, n)
		if *red > 0 {
			*red--
			finding := workflow.Finding{Severity: "high", Path: fmt.Sprintf("work%d.txt", n), Disposition: "open", Message: "wrong"}
			if err := e.review(att, workflow.ReviewStatusChangesRequested, finding); err != nil {
				e.t.Fatal(err)
			}
			e.tick()
			e.step()
			continue
		}
		if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
			e.t.Fatal(err)
		}
		e.tick()
		e.step()
		return
	}
}

// crashAfterFirstPrompt sends the first prompt, lets its acknowledgement
// window pass, and restarts the controller.
func (e *evalEnv) crashAfterFirstPrompt() {
	e.t.Helper()
	e.tick()
	e.clk.advance(state.AckWindow + time.Minute)
	e.tick()
	e.c = e.newController()
	if err := e.c.Recover(ctx, e.runID); err != nil {
		e.t.Fatalf("Recover: %v", err)
	}
	e.tick()
}

// runEval plays a scenario end to end and returns its result row.
func runEval(t *testing.T, sc evalrow.Scenario, arm, pair string) evalrow.Row {
	t.Helper()
	e := newEvalEnv(t, sc)
	// Run ids are deterministic, and distinct for each scenario and pair, so a
	// row names one run.
	e.ids = int(crc32.ChecksumIEEE([]byte(sc.ID+"/"+pair))%100000) * 1000
	if err := e.begin(); err != nil && e.runID == "" {
		t.Fatalf("Start: %v", err)
	}
	switch {
	case sc.Crash:
		e.crashAfterFirstPrompt()
	default:
		red := sc.RedReviews
		for _, task := range e.tasks() {
			if st, _ := e.runState(); st != "running" {
				break
			}
			e.playTask(task.ID, task.Ordinal, &red)
		}
	}
	return e.row(arm, pair)
}

func (e *evalEnv) row(arm, pair string) evalrow.Row {
	e.t.Helper()
	tasks := e.tasks()
	st, code := e.runState()
	r := evalrow.Row{Scenario: e.sc.ID, PairID: pair, Arm: arm, RunID: e.runID, Adapter: "codex", Model: "fixture-model",
		Mode: "fixture", State: st, PauseCode: code, LatencyBasis: "fixture-clock", SourceManifest: filepath.ToSlash(filepath.Join("runs", pair+"-"+arm+".json"))}
	run, _ := e.st.GetRun(ctx, e.runID)
	r.ConfigHash = run.ConfigHash
	for _, task := range tasks {
		if task.AcceptedAttemptID != "" {
			r.AcceptedTasks++
		}
	}
	r.PendingTasks = len(tasks) - r.AcceptedTasks
	if st != "finished" && r.PendingTasks > 0 {
		r.FailedTasks, r.PendingTasks = 1, r.PendingTasks-1
	}
	if u, err := e.c.CollectUsage(ctx, e.runID); err == nil {
		r.InputTokens, r.CachedInputTokens, r.OutputTokens, r.UsageCompleteness = u.Input, u.CachedInput, u.Output, u.Completeness
		if u.Completeness != workflow.UsageComplete {
			r.Reason = "usage is a lower bound: a child log could not be read"
		}
	} else {
		r.UsageCompleteness, r.Reason = workflow.UsageUnavailable, err.Error()
	}
	evs, err := e.st.RunEvents(ctx, e.runID)
	if err != nil {
		e.t.Fatal(err)
	}
	r.Events = len(evs)
	r.DurationMS = e.clk.now().Sub(e.start).Milliseconds()
	return r
}
