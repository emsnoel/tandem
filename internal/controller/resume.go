package controller

// Resuming a run. A run that stopped part way is archived, and a new run of
// the same plan starts at the task it stopped on. The tasks the earlier run
// accepted are carried over: the new run's task rows name the attempts that
// were accepted, so their check, review and waivers are the same evidence,
// and a teardown signs them off on it. Nothing is carried unless that evidence
// still holds, and the checkout holds exactly the files the earlier run left.

import (
	"context"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// ResumeLatest asks Start to resume the most recently archived run of the plan.
const ResumeLatest = "latest"

// ErrCannotResume: the run asked for cannot be resumed. Nothing was created,
// and the message says why and what to do.
var ErrCannotResume = errors.New("cannot resume")

// resumePoint is what a new run takes from the run it resumes.
type resumePoint struct {
	// From is the run resumed.
	From string
	// Accepted maps each carried task to its accepted attempt.
	Accepted map[string]string
	// Repair, when set, is the attempt whose open findings the first attempt
	// of the new run is given, as a repair would be.
	Repair *workflow.Attempt
}

// resumeRules are the parts of the new run's configuration that decide
// whether the carried evidence counts.
type resumeRules struct {
	CheckCommand        string
	RequireVerification bool
}

func cannotResume(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrCannotResume}, a...)...)
}

// applyResume carries into tasks what the run req.ResumeFrom names, and
// returns the note the new run's first attempt opens with. Without
// req.ResumeFrom it changes nothing. It runs before anything is created, so a
// refusal leaves nothing behind.
func (c *Controller) applyResume(ctx context.Context, req StartRequest, planHash, tree, policyHash string, tasks []state.TaskRow) (resumePoint, repairContext, error) {
	if req.ResumeFrom == "" {
		return resumePoint{}, repairContext{}, nil
	}
	rules := resumeRules{CheckCommand: req.CheckCommand, RequireVerification: policyHash != ""}
	rp, err := c.planResume(ctx, req.ResumeFrom, planHash, tree, tasks, rules)
	if err != nil {
		return rp, repairContext{}, err
	}
	for i := range tasks {
		tasks[i].AcceptedAttemptID = rp.Accepted[tasks[i].ID]
	}
	if rp.Repair == nil {
		return rp, repairContext{}, nil
	}
	rc, err := c.resumeRepair(ctx, rp.From, *rp.Repair)
	return rp, rc, err
}

// planResume decides what a new run of the plan with planHash takes from the
// run named by from (ResumeLatest or a run id), given tree, the checkout as it
// is now, and tasks, the new run's tasks. It writes nothing.
func (c *Controller) planResume(ctx context.Context, from, planHash, tree string, tasks []state.TaskRow, rules resumeRules) (resumePoint, error) {
	old, err := c.resumeSource(ctx, from, planHash)
	if err != nil {
		return resumePoint{}, err
	}
	oldCfg, err := c.config(ctx, old)
	if err != nil {
		return resumePoint{}, err
	}
	if oldCfg.CheckCommand != rules.CheckCommand {
		return resumePoint{}, cannotResume("run %s checked its work with %q and this run would check it with %q, so its checks do not count here. Set check_command back, or hand off without --resume to start at task 1",
			old.ID, oldCfg.CheckCommand, rules.CheckCommand)
	}
	oldTasks, err := c.d.State.RunTasks(ctx, old.ID)
	if err != nil {
		return resumePoint{}, err
	}
	byID := map[string]state.TaskRow{}
	for _, t := range oldTasks {
		byID[t.ID] = t
	}
	obs := workflow.Observed{NoCheckPolicy: rules.CheckCommand == "", RequireVerification: rules.RequireVerification}
	rp := resumePoint{From: old.ID, Accepted: map[string]string{}}
	var last *workflow.Attempt
	for _, t := range tasks {
		ot, ok := byID[t.ID]
		if !ok || ot.SpecHash != t.SpecHash {
			return resumePoint{}, cannotResume("task %s of run %s does not match this plan's", t.ID, old.ID)
		}
		if ot.AcceptedAttemptID == "" {
			continue
		}
		att, err := c.carriedAttempt(ctx, old.ID, ot, obs)
		if err != nil {
			return resumePoint{}, err
		}
		rp.Accepted[t.ID] = att.ID
		if last == nil || *att.AcceptedMS > *last.AcceptedMS {
			last = &att
		}
	}
	if len(rp.Accepted) == len(tasks) {
		return resumePoint{}, cannotResume("run %s accepted every task of the plan, so nothing is left to build", old.ID)
	}
	open, err := c.openFindings(ctx, old.ID, oldTasks)
	if err != nil {
		return resumePoint{}, err
	}
	switch {
	case open != nil && open.EndSnapshotHash == tree:
		rp.Repair = open
		return rp, nil
	case last != nil && last.EndSnapshotHash == tree:
		return rp, nil
	case last == nil && open == nil:
		return resumePoint{}, cannotResume("run %s accepted no task and left no review findings to carry, so there is nothing to resume. Hand off without --resume to start at task 1", old.ID)
	}
	var left string
	if last != nil {
		left = fmt.Sprintf("the tree %s was accepted on", last.TaskID)
	}
	if open != nil {
		if left != "" {
			left += ", and from "
		}
		left += fmt.Sprintf("the tree attempt %d of %s left for repair", open.Ordinal, open.TaskID)
	}
	return resumePoint{}, cannotResume("the files in the checkout are not the ones run %s left: they differ from %s. git status shows what changed. Put the files back as the run left them, or hand off without --resume to start at task 1",
		old.ID, left)
}

// resumeSource is the archived run of this plan that from names.
func (c *Controller) resumeSource(ctx context.Context, from, planHash string) (state.Run, error) {
	id := from
	if from == ResumeLatest {
		found, ok, err := c.d.State.LatestArchivedRun(ctx, planHash)
		if err != nil {
			return state.Run{}, err
		}
		if !ok {
			return state.Run{}, cannotResume("this checkout has no archived run of this plan. A stopped run is archived with tandem state archive; tandem-pause-note prints the steps. Hand off without --resume to start at task 1")
		}
		id = found
	} else if !idPattern.MatchString(from) {
		return state.Run{}, cannotResume("%q is not a run id; give the 32 characters tandem build status prints, or %s", from, ResumeLatest)
	}
	old, err := c.d.State.GetRun(ctx, id)
	if err != nil {
		return state.Run{}, cannotResume("there is no run %s in this checkout", id)
	}
	if old.State != "archived" {
		return state.Run{}, cannotResume("run %s is %s, and only an archived run can be resumed. Stop it and archive it first; tandem-pause-note prints the steps", old.ID, old.State)
	}
	if old.PlanHash != planHash {
		return state.Run{}, cannotResume("run %s built another version of this plan, so its accepted tasks are not this plan's. Hand off without --resume to start at task 1", old.ID)
	}
	return old, nil
}

// carriedAttempt is the attempt that accepted task t in run from, once its
// evidence still satisfies acceptance under the new run's rules and the patch
// and tree it was accepted on are intact.
func (c *Controller) carriedAttempt(ctx context.Context, from string, t state.TaskRow, obs workflow.Observed) (workflow.Attempt, error) {
	att, err := c.d.State.GetAttempt(ctx, t.AcceptedAttemptID)
	if err != nil {
		return att, err
	}
	if att.AcceptedMS == nil || att.TaskID != t.ID {
		return att, cannotResume("task %s of run %s names attempt %s, which was not accepted for it", t.ID, from, att.ID)
	}
	f, err := c.d.State.AcceptanceFacts(ctx, att, obs)
	if err != nil {
		return att, err
	}
	if unmet := workflow.Unmet(att, f); len(unmet) > 0 {
		return att, cannotResume("task %s of run %s no longer has the evidence that accepted it: it lacks %v", t.ID, from, unmet)
	}
	for _, h := range []string{att.PatchHash, att.EndSnapshotHash} {
		if _, err := c.d.Artifacts.Read(ctx, h, 0, 1); err != nil {
			return att, cannotResume("the patch or tree that accepted task %s of run %s is missing or damaged: %v", t.ID, from, err)
		}
	}
	return att, nil
}

// openFindings is the attempt of the task run stopped on whose failed check
// or requested changes were still open: the latest attempt of that task that
// handed back, when its check failed or its review did not pass. Nil when
// there is none.
func (c *Controller) openFindings(ctx context.Context, run string, tasks []state.TaskRow) (*workflow.Attempt, error) {
	for _, t := range tasks {
		if t.AcceptedAttemptID != "" {
			continue
		}
		as, err := c.d.State.TaskAttempts(ctx, run, t.ID)
		if err != nil {
			return nil, err
		}
		if len(as) == 0 {
			continue
		}
		for i := len(as) - 1; i >= 0; i-- {
			a := as[i]
			if a.EndSnapshotHash == "" {
				continue
			}
			if a.Check == workflow.CheckFailed || a.Review == workflow.ReviewChangesRequested || a.Review == workflow.ReviewFailed {
				return &a, nil
			}
			break
		}
		return nil, nil
	}
	return nil, nil
}

// resumeRepair is the note the first attempt of a resumed run opens with when
// the task it resumes was waiting on a repair.
func (c *Controller) resumeRepair(ctx context.Context, from string, parent workflow.Attempt) (repairContext, error) {
	lead := fmt.Sprintf("This task resumes run %s, which stopped while the task was being repaired. The reviewer asked for changes to attempt %d of that run; its work is still in the tree. This is a fresh conversation: fix what the reviewer found, starting from what follows.",
		from, parent.Ordinal)
	if parent.Check == workflow.CheckFailed {
		lead = fmt.Sprintf("This task resumes run %s, which stopped while the task was being repaired. The check failed on attempt %d of that run; its work is still in the tree. This is a fresh conversation: make the check pass, starting from what follows.",
			from, parent.Ordinal)
	}
	return c.repairNote(ctx, parent, lead)
}
