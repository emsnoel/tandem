package controller

import (
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// tandem cannot read agy's usage (#113), so every figure for an agy job is
// unknown, and an unknown figure against a limit pauses the run before each
// review. A run whose builder or reviewer kind has no usage reader therefore
// starts without limits: an unmeasurable limit only blocks work.
func TestARunWithAnUnmeasuredKindHasNoBudget(t *testing.T) {
	for _, c := range []struct {
		name, builder, reviewer string
		wantTask                int64
	}{
		{"agy builder", "agy", "", 0},
		{"agy reviewer", "codex", "agy", 0},
		{"measured kinds keep the limit", "codex", "", 3000000},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := usageEnv(t)
			id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
				r.BuilderKind, r.TaskInputLimit, r.RunInputLimit = c.builder, 3000000, 9000000
				if c.reviewer != "" {
					r.ReviewerKind = c.reviewer
				}
			}))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			e.runID = id
			run, err := e.st.GetRun(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			wantRun := int64(0)
			if c.wantTask != 0 {
				wantRun = 9000000
			}
			if run.TaskInputLimit != c.wantTask || run.RunInputLimit != wantRun {
				t.Errorf("limits %d/%d, want %d/%d", run.TaskInputLimit, run.RunInputLimit, c.wantTask, wantRun)
			}
		})
	}
}

// The case that paused a live agy pair: the builder's usage is unknown, and
// the review still starts.
func TestAnAgyBuilderReachesReviewWithUnknownUsage(t *testing.T) {
	e, _ := usageEnv(t)
	e.tr.noSession = true
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderKind, r.TaskInputLimit = "agy", 3000000 }))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	e.runID = id
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	e.wantRun("running", "")
}

func TestUnmeasuredKind(t *testing.T) {
	for _, c := range []struct {
		kinds []string
		want  string
	}{
		{[]string{"codex", "claude", "copilot"}, ""},
		{[]string{"codex", ""}, ""},
		{[]string{"claude", "agy"}, "agy"},
		{[]string{"agy", "codex"}, "agy"},
	} {
		if got := UnmeasuredKind(c.kinds...); got != c.want {
			t.Errorf("UnmeasuredKind(%q) = %q, want %q", c.kinds, got, c.want)
		}
	}
}
