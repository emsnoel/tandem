package controller

import (
	"testing"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/workflow"
)

// partlyKnownBlock leaves a run paused before a review because the builder's
// session is not known, so its limit cannot be checked.
func (e *cenv) partlyKnownBlock() workflow.Attempt {
	e.t.Helper()
	e.tr.noSession = true
	e.startLimited(3000000, 0)
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	e.tick()
	e.wantRun("paused", workflow.PauseScheduling)
	return att
}

func (e *cenv) sessionFound(src *fakeUsage, att workflow.Attempt, input int64) {
	e.t.Helper()
	jobs, err := e.st.AttemptJobs(ctx, att.ID, "builder")
	if err != nil || len(jobs) != 1 {
		e.t.Fatalf("builder jobs %v %v", jobs, err)
	}
	if err := e.st.SetBuilderJobSession(ctx, jobs[0].ID, "found-later"); err != nil {
		e.t.Fatal(err)
	}
	src.streams["found-later"] = []rollout.Stream{stream("found-later", "", ev("found-later", 3, input, 0, 1))}
}

func TestBudgetBlockFromUnknownUsageClearsOnceUsageIsKnown(t *testing.T) {
	e, src := usageEnv(t)
	att := e.partlyKnownBlock()

	e.sessionFound(src, att, 500)
	e.tick() // the usage is read and the block is looked at again
	e.wantRun("running", "")
	e.tick()
	e.tick()
	if e.reviewerJobs(att) != 1 || e.delivered()["review_prompt"] != 1 {
		t.Errorf("%d reviewer jobs, %d review prompts: the held review runs once", e.reviewerJobs(att), e.delivered()["review_prompt"])
	}
}

func TestBudgetBlockStaysWhenKnownUsageReachesTheLimit(t *testing.T) {
	e, src := usageEnv(t)
	att := e.partlyKnownBlock()

	e.sessionFound(src, att, 3000000)
	e.tick()
	e.tick()
	e.wantRun("paused", workflow.PauseScheduling)
	if e.reviewerJobs(att) != 0 {
		t.Error("a review started with the budget exhausted")
	}
}
