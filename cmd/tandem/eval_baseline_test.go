package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/evalrow"
)

// The baseline arm of the evaluation is the legacy engine. Its tasks advance
// through `tandem build advance` with the agent's state and the check scripted.

const evalScenarios = "../../internal/evalrow/testdata/scenarios.json"

// legacyCannot names what the legacy engine has no way to express, so a
// scenario about it has no baseline row to pair with.
func legacyCannot(sc evalrow.Scenario) string {
	switch {
	case sc.Crash:
		return "the legacy engine has no acknowledged delivery to lose"
	case sc.ContextLimit > 0 || sc.BulkyConstraints:
		return "the legacy engine has no prompt size limit"
	}
	return ""
}

func baselineRow(t *testing.T, sc evalrow.Scenario, pair string) evalrow.Row {
	t.Helper()
	text := sc.Plan()
	root, dir := buildRepoPlan(t, text, "./check.sh", 0)
	f := &fakeAdvance{statuses: []string{"done"}}
	red := sc.RedReviews
	steps := 0
	for n := 1; n <= sc.Tasks; n++ {
		f.usage = &build.Usage{Calls: 1, Input: 1000, First: 1000, Peak: 1000}
		if sc.PartialUsage && n == 2 {
			f.usage = nil
		}
		f.exit = 0
		if red > 0 {
			f.exit, red = 1, red-1
		}
		got := advance(t, f, root, "--task", fmt.Sprint(n))
		steps++
		if strings.HasPrefix(got, "stopped red") {
			f.exit = 0
			got = advance(t, f, root, "--task", fmt.Sprint(n))
			steps++
		}
		// Every task is reviewed in the reviewer pane, which hands back a
		// clean verdict here.
		if strings.HasPrefix(got, "review ") {
			got = advance(t, f, root, "--task", fmt.Sprint(n), "--event", fmt.Sprintf("review-%d", n), "--review", "--verdict", "clean", "--from", "reviewer")
			steps++
		}
		if !strings.HasPrefix(got, "next ") && !strings.HasPrefix(got, "finished") {
			break
		}
	}
	b := load(t, dir)
	sum := sha256.Sum256([]byte("legacy\n" + text))
	r := evalrow.Row{Scenario: sc.ID, PairID: pair, Arm: "baseline", RunID: "legacy-" + sc.ID + "-" + pair, Adapter: "codex", Model: "fixture-model",
		ConfigHash: hex.EncodeToString(sum[:]), Mode: "fixture", State: b.State, LatencyBasis: "fixture-clock",
		DurationMS: int64(steps) * 1000, Events: steps, UsageCompleteness: "complete",
		SourceManifest: "runs/" + pair + "-baseline.json"}
	var in int64
	for _, task := range b.Tasks {
		if task.Status == build.StatusDone {
			r.AcceptedTasks++
		}
		if task.Usage == nil {
			if task.Status == build.StatusDone {
				r.UsageCompleteness, r.Reason = "partial", "a task has no measured usage"
			}
			continue
		}
		in += task.Usage.Input
	}
	r.InputTokens, r.PendingTasks = &in, len(b.Tasks)-r.AcceptedTasks
	if b.State != build.StateFinished && r.PendingTasks > 0 {
		r.FailedTasks, r.PendingTasks = 1, r.PendingTasks-1
	}
	return r
}

func TestEvalBaselineRunsThroughTheLegacyEntrypoints(t *testing.T) {
	f, err := evalrow.Load(evalScenarios)
	if err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for _, sc := range f.Scenarios {
		if why := legacyCannot(sc); why != "" {
			skipped++
			continue
		}
		r := baselineRow(t, sc, "pair-001")
		if r.State != build.StateFinished || r.AcceptedTasks != sc.Tasks || r.FailedTasks != 0 {
			t.Errorf("%s: %+v", sc.ID, r)
		}
		if r.CachedInputTokens != nil {
			t.Errorf("%s: the legacy usage has no cached figure, so none may be reported", sc.ID)
		}
		if sc.PartialUsage != (r.UsageCompleteness == "partial") {
			t.Errorf("%s: usage %s", sc.ID, r.UsageCompleteness)
		}
	}
	if skipped != 2 {
		t.Errorf("%d scenarios have no baseline arm, want crash and large-required", skipped)
	}
}

// TestEvalBaselineRows writes one baseline-arm row per scenario the legacy
// engine can express to TANDEM_EVAL_OUT, for the token eval kept outside this
// repository. It does nothing in an ordinary test run.
func TestEvalBaselineRows(t *testing.T) {
	out := os.Getenv("TANDEM_EVAL_OUT")
	if out == "" {
		t.Skip("TANDEM_EVAL_OUT names the file the rows go to")
	}
	f, err := evalrow.Load(evalScenarios)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range f.Scenarios {
		if legacyCannot(sc) != "" {
			continue
		}
		if err := evalrow.Append(out, baselineRow(t, sc, os.Getenv("TANDEM_EVAL_PAIR"))); err != nil {
			t.Fatal(err)
		}
	}
}
