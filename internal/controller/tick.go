package controller

import (
	"context"
	"errors"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Run pause codes the controller sets beyond the workflow's.
const (
	pauseSourceMutated = "source_mutated"
	pauseWriterUnknown = "writer_unknown"
	pauseRepairs       = "repairs_exhausted"
	pauseDispatch      = "dispatch_failed"
	pauseReview        = "review_unknown"
	pauseStopped       = "stopped"
	pausePlanChanged   = "plan_changed"
	pauseContext       = "context_too_large"
	pauseMemory        = "memory_conflict"
	pauseIncomplete    = "context_incomplete"
	pauseAgentExited   = "agent_exited"
	pauseNoReviewer    = "no_reviewer"
)

// externalKinds are delivered to an agent through the transport; the rest are
// local steps the controller performs itself.
var externalKinds = []string{workflow.EffectBuilderDispatch, effectReviewPrompt, effectVerifyPrompt, effectNotify, workflow.EffectCancel}

var internalKinds = []string{workflow.EffectCheckDispatch, workflow.EffectReviewDispatch, workflow.EffectCreateRepairAttempt, workflow.EffectAdvanceRun}

const (
	maxMailboxPerTick = 100
	maxSettlePasses   = 12
)

// Tick makes one bounded pass over a run: it ingests queued builder messages,
// advances whatever local steps are ready, and delivers at most one external
// effect. It returns when it has nothing more it can do right now. It needs
// the writer lease; a controller that does not hold it gets ErrNotWriter.
func (c *Controller) Tick(ctx context.Context, runID string) error {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.State == "finished" {
		return nil
	}
	// Renew first: a controller that was idle past its TTL, with nobody else
	// having taken over, is still the writer. Anyone else, or a fence other than
	// the one this controller was given, is not.
	held := c.fence(run.CheckoutID)
	if held == 0 {
		// A new process of the same logical controller resumes the lease it
		// already holds; any other owner, or a lease being reconciled, is not it.
		if lease, err := c.d.State.LeaseState(ctx, run.CheckoutID); err == nil && lease.Owner == c.owner && lease.State == "owned" {
			held = lease.Fence
			c.setFence(run.CheckoutID, held)
		}
	}
	if err := c.d.State.RenewLease(ctx, run.CheckoutID, c.owner, held, leaseTTL); err != nil || held == 0 {
		if run.State == "paused" {
			return nil // nothing to drive, and no writer to protect
		}
		if err != nil && !errors.Is(err, state.ErrStaleFence) {
			return err
		}
		return ErrNotWriter
	}
	// Usage is collected after every pass, settlement, pause and stop among
	// them, budgets or not; only what is new in each log is read.
	return errors.Join(c.pass(ctx, runID), c.collectAfter(ctx, runID))
}

func (c *Controller) pass(ctx context.Context, runID string) error {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	mailErr := c.processMailbox(ctx, run, cfg)
	if run, err = c.d.State.GetRun(ctx, runID); err != nil {
		return err
	}
	// A staged review result is applied here, not by the mailbox. A run that
	// was paused when this pass began applies it and schedules nothing more
	// until the next pass, even if applying it cleared the pause.
	// A recovery policy handles a paused run's failure before anything else is
	// scheduled: it repairs once or escalates once, and may let the run go on.
	if rerr := c.recoverPaused(ctx, run, cfg); rerr != nil {
		mailErr = errors.Join(mailErr, rerr)
	}
	if run, err = c.d.State.GetRun(ctx, runID); err != nil {
		return err
	}
	wasPaused := run.State == "paused"
	if rerr := c.reconcileReviews(ctx, run, cfg); rerr != nil {
		mailErr = errors.Join(mailErr, rerr)
	}
	if rerr := c.readPaneReviews(ctx, run, cfg); rerr != nil {
		mailErr = errors.Join(mailErr, rerr)
	}
	if rerr := c.sendReviewRejections(ctx, run, cfg); rerr != nil {
		mailErr = errors.Join(mailErr, rerr)
	}
	if wasPaused {
		return mailErr
	}
	if run, err = c.d.State.GetRun(ctx, runID); err != nil {
		return err
	}
	switch run.State {
	case "stopping":
		return errors.Join(mailErr, c.tickStopping(ctx, run, cfg))
	case "paused", "finished":
		return mailErr
	}
	// A run an older tandem started has no reviewer pane in its pinned
	// configuration, and the builder never reviews. It waits for a person
	// rather than sending a review prompt to no agent.
	if cfg.ReviewerSession == "" {
		return errors.Join(mailErr, c.d.State.SetRunState(ctx, run.ID, "paused", pauseNoReviewer))
	}

	for i := 0; i < maxSettlePasses; i++ {
		progressed, err := c.settle(ctx, run, cfg)
		if err != nil {
			return errors.Join(mailErr, err)
		}
		if !progressed {
			break
		}
		if run, err = c.d.State.GetRun(ctx, runID); err != nil {
			return err
		}
		if run.State != "running" {
			return mailErr
		}
	}
	if err := c.deliver(ctx, run, cfg, externalKinds); err != nil {
		return errors.Join(mailErr, err)
	}
	return mailErr
}

func (c *Controller) load(ctx context.Context, runID string) (state.Run, runConfig, error) {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return run, runConfig{}, err
	}
	cfg, err := c.config(ctx, run)
	return run, cfg, err
}

// settle advances every local step that is ready and reports whether anything
// changed. Acceptance, repair and the next task's start live here; check and
// review dispatch are effects processed below.
func (c *Controller) settle(ctx context.Context, run state.Run, cfg runConfig) (bool, error) {
	a, err := c.drive(ctx, run, cfg)
	if err != nil {
		return a, err
	}
	b, err := c.processInternal(ctx, run, cfg)
	if err != nil {
		return a || b, err
	}
	d, err := c.runDirectReviews(ctx, run, cfg)
	return a || b || d, err
}

// drive takes the next step of the task being worked on. That is the unfinished
// task that already has an attempt, if there is one, and otherwise the first
// task in plan order whose dependencies are all accepted: the plan's order
// breaks ties, and its dependencies decide what may start.
func (c *Controller) drive(ctx context.Context, run state.Run, cfg runConfig) (bool, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return false, err
	}
	accepted := map[string]bool{}
	for _, t := range tasks {
		accepted[t.ID] = t.AcceptedAttemptID != ""
	}
	var next *state.TaskRow
	var attempts []workflow.Attempt
	for i := range tasks {
		t := tasks[i]
		if accepted[t.ID] {
			continue
		}
		as, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return false, err
		}
		if len(as) > 0 {
			next, attempts = &tasks[i], as
			break
		}
		if next == nil && ready(t, accepted) {
			next, attempts = &tasks[i], nil
		}
	}
	if next == nil {
		return false, nil
	}
	t := *next
	var cur *workflow.Attempt
	for i := range attempts {
		if attempts[i].Current {
			cur = &attempts[i]
		}
	}
	switch {
	case len(attempts) == 0:
		if hold, err := c.holdForBudget(ctx, run, cfg, t.ID); err != nil || hold {
			return false, err
		}
		snap, err := c.freshStart(ctx, cfg)
		if err != nil {
			return false, err
		}
		return true, c.startAttempt(ctx, run, cfg, t, 1, "", snap, repairContext{})
	case cur == nil:
		// Superseded by a repair whose new attempt is still to be created.
		return false, nil
	case cur.Version == 0 && cur.Implementation == workflow.ImplementationPending:
		return true, c.ensureStarted(ctx, run, cfg, *cur)
	case cur.Implementation == workflow.ImplementationSubmitted:
		return c.advanceSubmitted(ctx, run, cfg, *cur)
	}
	return false, nil
}

// ready reports whether every task t depends on has been accepted.
func ready(t state.TaskRow, accepted map[string]bool) bool {
	for _, d := range t.DependsOn {
		if !accepted[d] {
			return false
		}
	}
	return true
}

func (c *Controller) freshStart(ctx context.Context, cfg runConfig) (string, error) {
	return c.captureStart(ctx, cfg)
}

// advanceSubmitted accepts an attempt whose evidence is complete, or starts
// its repair when the reviewer asked for changes.
func (c *Controller) advanceSubmitted(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (bool, error) {
	checkOK := att.Check == workflow.CheckPassed || att.Check == workflow.CheckNotApplicable
	reviewOK := att.Review == workflow.ReviewPassed || att.Review == workflow.ReviewWaived || att.Review == workflow.ReviewNotApplicable
	switch {
	case checkOK && reviewOK:
		// A run with a production policy needs the planner's independent
		// verification as well. The gate decides whether to wait, repair, pause
		// or go on to accept.
		if proceed, progressed, err := c.verificationGate(ctx, run, cfg, att); err != nil || !proceed {
			return progressed, err
		}
		// What is accepted is the tree that was checked and reviewed. If the
		// checkout has moved since, acceptance waits for a person.
		now, err := c.captureStart(ctx, cfg)
		if err != nil {
			return false, err
		}
		if now != att.EndSnapshotHash {
			return true, c.d.State.SetRunState(ctx, run.ID, "paused", pauseSourceMutated)
		}
		payload, err := c.putJSON(ctx, map[string]string{"accepted_attempt": att.ID})
		if err != nil {
			return false, err
		}
		_, err = c.apply(ctx, run, cfg, att, derive(att.ID, "accept"), workflow.EventAcceptRequested, payload)
		if err != nil {
			return false, err
		}
		if c.hooks.afterAccept != nil {
			if err := c.hooks.afterAccept(); err != nil {
				return true, err
			}
		}
		return true, nil
	case checkOK && att.Review == workflow.ReviewChangesRequested:
		return c.startRepair(ctx, run, cfg, att)
	}
	return false, nil
}

// startRepair ends the attempt and asks for its repair, or pauses when the
// task has used its attempts.
func (c *Controller) startRepair(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (bool, error) {
	if cfg.RecoveryPolicy != "" {
		// Bounded recovery decides: one automatic repair per piece of work,
		// across runs, or a single decision for the planner.
		return c.recoverFailure(ctx, run, cfg, att)
	}
	if att.Ordinal >= state.MaxAttempts {
		return true, c.d.State.SetRunState(ctx, run.ID, "paused", pauseRepairs)
	}
	payload, err := c.putJSON(ctx, map[string]string{"repair_of": att.ID})
	if err != nil {
		return false, err
	}
	_, err = c.apply(ctx, run, cfg, att, derive(att.ID, "repair"), workflow.EventRepairStarted, payload)
	return err == nil, err
}

// verificationGate decides what the independent verification allows. It
// reports whether acceptance may go on, and whether it changed anything.
func (c *Controller) verificationGate(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) (proceed, progressed bool, err error) {
	// The reviewer pane's review is the independent check.
	if decided, proceed, progressed, err := c.reviewerGate(ctx, run, att); err != nil || decided {
		return proceed, progressed, err
	}
	if cfg.AcceptancePolicy == "" {
		return true, false, nil
	}
	v, err := c.d.State.AttemptVerification(ctx, att.ID)
	if err != nil {
		return false, false, err
	}
	switch {
	case !v.Found:
		created, err := c.ensureVerification(ctx, run, cfg, att)
		return false, created, err
	case !v.Distinct:
		// A result from the writer's own side is recorded and counts for nothing.
		return false, true, c.d.State.SetRunState(ctx, run.ID, "paused", pauseVerificationIdentity)
	case !v.Valid:
		return false, true, c.d.State.SetRunState(ctx, run.ID, "paused", pauseVerificationStale)
	case v.Status == workflow.ReviewChangesRequested:
		progressed, err := c.startRepair(ctx, run, cfg, att)
		return false, progressed, err
	case v.Status == workflow.ReviewFailed:
		return false, true, c.d.State.SetRunState(ctx, run.ID, "paused", workflow.PauseReviewFailed)
	case v.Status == workflow.ReviewPassed:
		return true, false, nil
	}
	return false, false, nil
}

// apply issues a controller event for an attempt at the version the controller
// reads, re-reading a few times if it moves.
func (c *Controller) apply(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt, eventID, kind, payload string) (workflow.Receipt, error) {
	return c.applyObserved(ctx, run, att, eventID, kind, payload, c.observed(ctx, run, cfg))
}

// applyObserved is apply with the facts the controller observed given.
func (c *Controller) applyObserved(ctx context.Context, run state.Run, att workflow.Attempt, eventID, kind, payload string, obs workflow.Observed) (workflow.Receipt, error) {
	var err error
	for try := 0; try < 3; try++ {
		var cur workflow.Attempt
		if cur, err = c.d.State.GetAttempt(ctx, att.ID); err != nil {
			return workflow.Receipt{}, err
		}
		var rec workflow.Receipt
		rec, err = c.d.State.ApplyWith(ctx, workflow.Event{ID: eventID, RunID: run.ID, AttemptID: att.ID, Kind: kind,
			PayloadHash: payload, ExpectedVersion: cur.Version}, obs)
		if err == nil || !errors.Is(err, workflow.ErrConflict) {
			return rec, err
		}
	}
	return workflow.Receipt{}, err
}
