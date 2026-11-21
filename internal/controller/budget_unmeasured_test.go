package controller

import (
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Usage tandem cannot read counts as unknown, and an unknown figure against a
// limit pauses the run before each review. A run whose builder or reviewer kind
// has no usage reader therefore starts without limits: an unmeasurable limit
// only blocks work.
func TestARunWithAnUnmeasuredKindHasNoBudget(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, builder, reviewer string
		wantTask                int64
	}{
		{"unmeasured builder", "nonesuch", "", 0},
		{"unmeasured reviewer", "codex", "nonesuch", 0},
		{"agy builder keeps the limit", "agy", "", 3000000},
		{"agy reviewer keeps the limit", "codex", "agy", 3000000},
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

// An agy builder with a budget measures usage from its conversation log and
// reaches review when usage is under the limit.
func TestAnAgyBuilderReachesReviewWithMeasuredUsage(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	home := t.TempDir()
	e.usage = AgentUsageSource{AgyHome: home}
	e.c = e.newController()
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.BuilderKind, r.TaskInputLimit = "agy", 3000000 }))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	e.runID = id
	e.tick()
	att := e.current("task-1")
	session := e.sessionOf(att)
	writeAgyDB(t, home, session,
		[]agyStepRow{{stepType: agyStepUserInput, payload: agyUserStepBlob("build task 1")}},
		[][]byte{agyGenMetaBlob("gemini-4-neon", "", "resp_1", "req-1", 1000, 500, 100, false)})
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	e.wantRun("running", "")
}

func TestUnmeasuredKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kinds []string
		want  string
	}{
		{[]string{"codex", "claude", "copilot", "agy"}, ""},
		{[]string{"codex", ""}, ""},
		{[]string{"claude", "nonesuch"}, "nonesuch"},
		{[]string{"nonesuch", "codex"}, "nonesuch"},
	} {
		if got := UnmeasuredKind(c.kinds...); got != c.want {
			t.Errorf("UnmeasuredKind(%q) = %q, want %q", c.kinds, got, c.want)
		}
	}
}
