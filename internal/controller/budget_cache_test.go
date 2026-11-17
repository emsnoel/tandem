package controller

import (
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/workflow"
)

// A task limit counts a cache read at a tenth of a token. A task that re-reads
// a large cached context many times goes on to its review. A task that adds
// as much fresh input stops before it, and the pause note names both figures.
func TestBudgetCountsCacheReadsAtATenth(t *testing.T) {
	for _, c := range []struct {
		name           string
		input, cached  int64
		blocked        bool
		reasonContains string
	}{
		{name: "mostly cache", input: 9150, cached: 8890},
		{name: "mostly fresh", input: 3900, cached: 1000, blocked: true, reasonContains: "2900 fresh and 1000 cached"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, src := usageEnv(t)
			e.startLimited(3000, 0)
			e.tick()
			att := e.current("task-1")
			s := e.sessionOf(att)
			src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 3, c.input, c.cached, 1))}
			e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
			e.work(1)
			e.settle(att)
			e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
			e.tick()
			e.tick()

			if !c.blocked {
				if e.pause() != "running/" || e.reviewerJobs(att) != 1 {
					t.Errorf("run %s with %d reviewer jobs: cached input stopped the review", e.pause(), e.reviewerJobs(att))
				}
				return
			}
			if e.pause() != "paused/scheduling_blocked" || e.reviewerJobs(att) != 0 {
				t.Fatalf("run %s with %d reviewer jobs: fresh input did not stop the review", e.pause(), e.reviewerJobs(att))
			}
			// Setting the same limit again reports what the budget says now.
			d, err := e.c.SetBudget(ctx, e.runID, "task", 3000)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(d.Reason, c.reasonContains) {
				t.Errorf("reason %q does not name %q", d.Reason, c.reasonContains)
			}
		})
	}
}
