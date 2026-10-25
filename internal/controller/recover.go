package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Stop records the intent to stop. It needs no lease: it persists a cancel
// event, which moves the run to stopping and queues a cancel message. Nothing
// is released until the writer is observed to have terminated.
func (c *Controller) Stop(ctx context.Context, runID string) error {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	if run.State == "finished" {
		return nil
	}
	msg, err := c.put(ctx, "[tandem] The run was stopped. Stop what you are doing and leave the tree as it is. Do not hand anything back.\n")
	if err != nil {
		return err
	}
	// A repeated stop is a replay and harmless. A run that was stopping and then
	// paused for another reason gets a new stop keyed by its version, so its open
	// job cannot block the archive.
	stopID := derive(runID, "stop")
	if run.State == "paused" && run.PauseCode != pauseStopped {
		stopID = derive(runID, "stop", strconv.FormatInt(run.Version, 10))
	}
	_, err = c.d.State.ApplyWith(ctx, workflow.Event{ID: stopID, RunID: runID, Kind: workflow.EventStopRequested,
		PayloadHash: msg, ExpectedVersion: run.Version}, c.observed(ctx, run, cfg))
	if errors.Is(err, workflow.ErrConflict) {
		// A stop already recorded, issued at an older run version, is a replay.
		if again, gerr := c.d.State.GetRun(ctx, runID); gerr == nil && again.State == "stopping" {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	return c.collectAfter(ctx, runID)
}

// tickStopping delivers the cancel message and, once the builder is observed
// to have stopped, releases the writer. While it is still running, or its state
// is unknown, the lease stays held and nothing new starts.
func (c *Controller) tickStopping(ctx context.Context, run state.Run, cfg runConfig) error {
	if _, err := c.d.State.CancelPendingEffects(ctx, run.ID, []string{workflow.EffectBuilderDispatch, effectReviewPrompt, effectNotify, effectRecoveryDecision}); err != nil {
		return err
	}
	if err := c.deliver(ctx, run, cfg, []string{workflow.EffectCancel}); err != nil {
		return err
	}
	quiet, err := c.writerQuiet(ctx, run)
	if err != nil || !quiet {
		return err
	}
	// The builder is seen to have stopped, so the job it was doing is over.
	if err := c.d.State.SettleBuilderJobs(ctx, run.ID, true); err != nil {
		return err
	}
	if err := c.d.State.ReleaseLease(ctx, run.CheckoutID, c.owner, c.fence(run.CheckoutID)); err != nil {
		return err
	}
	return c.d.State.SetRunState(ctx, run.ID, "paused", pauseStopped)
}

// writerQuiet reports whether no agent is still working for the run: every
// prompt that was sent is observed to have completed or stopped.
func (c *Controller) writerQuiet(ctx context.Context, run state.Run) (bool, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, e := range effects {
		if e.Kind != workflow.EffectBuilderDispatch {
			continue
		}
		switch e.State {
		case "delivering", "acknowledged", "delivery_unknown":
			obs, err := c.d.Transport.Observe(ctx, e.ID)
			if err != nil {
				return false, err
			}
			if obs.State != runner.ObsCompleted && obs.State != runner.ObsStopped {
				return false, nil
			}
		}
	}
	return true, nil
}

// Recover brings a run back after a restart. It reconciles the previous writer
// before taking the lease: an expired lease is a question, not an opening. An
// unconfirmed prompt is resolved only by its nonce. If a writer may still be
// running, the run pauses as writer_unknown. Otherwise it continues with a
// Tick.
func (c *Controller) Recover(ctx context.Context, runID string) error {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	// A run an older tandem drove can hold builder jobs that were never ended
	// when their attempt settled, which keeps it from being archived.
	stopped := run.State == "paused" && run.PauseCode == pauseStopped
	if run.State != "archived" {
		if err := c.d.State.SettleBuilderJobs(ctx, run.ID, stopped); err != nil {
			return err
		}
	}
	// A finished run has nothing to drive, and a stopped one released its
	// writer on purpose: taking the lease back would only block its archive.
	if run.State == "finished" || stopped {
		return nil
	}
	if err := c.checkPlan(ctx, run); err != nil {
		return err
	}
	fence, err := c.d.State.AcquireLease(ctx, run.CheckoutID, c.owner, leaseTTL)
	if errors.Is(err, workflow.ErrBusy) {
		lease, lerr := c.d.State.LeaseState(ctx, run.CheckoutID)
		if lerr != nil {
			return lerr
		}
		if lease.State != "reconciling" {
			return err
		}
		clear, rerr := c.reconcileWriter(ctx, run)
		if rerr != nil {
			return rerr
		}
		if !clear {
			return c.d.State.SetRunState(ctx, run.ID, "paused", pauseWriterUnknown)
		}
		if err := c.d.State.BreakLease(ctx, run.CheckoutID, lease.Fence); err != nil {
			return err
		}
		fence, err = c.d.State.AcquireLease(ctx, run.CheckoutID, c.owner, leaseTTL)
	}
	if err != nil {
		return err
	}
	c.setFence(run.CheckoutID, fence)
	if run, err = c.d.State.GetRun(ctx, runID); err != nil {
		return err
	}
	// A run paused only because its writer was unknown may continue once the
	// writer is known; any other pause waits for a person.
	if run.State == "paused" && run.PauseCode == pauseWriterUnknown {
		unknown, err := c.hasUnknownWriter(ctx, run)
		if err != nil {
			return err
		}
		if !unknown {
			if err := c.d.State.SetRunState(ctx, run.ID, "running", ""); err != nil {
				return err
			}
		}
	}
	// A run paused because its builder or reviewer quit continues once the host
	// finds both again; while either is still gone it stays paused.
	if run.State == "paused" && run.PauseCode == pauseAgentExited {
		cfg, err := c.config(ctx, run)
		if err != nil {
			return err
		}
		if c.goneAgent(ctx, cfg) == "" {
			if err := c.d.State.SetRunState(ctx, run.ID, "running", ""); err != nil {
				return err
			}
		}
	}
	return c.pass(ctx, runID)
}

// hasUnknownWriter reports whether a builder prompt's delivery is unconfirmed,
// after trying to confirm each by observation.
func (c *Controller) hasUnknownWriter(ctx context.Context, run state.Run) (bool, error) {
	clear, err := c.reconcileWriter(ctx, run)
	return !clear, err
}

// reconcileWriter observes every unconfirmed builder prompt of the run. A
// prompt whose nonce is seen on a live agent is acknowledged. It returns true
// when no unconfirmed prompt is left.
func (c *Controller) reconcileWriter(ctx context.Context, run state.Run) (bool, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return false, err
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].ID < effects[j].ID })
	clear := true
	for _, e := range effects {
		if e.Kind != workflow.EffectBuilderDispatch || (e.State != "delivering" && e.State != "delivery_unknown") {
			continue
		}
		obs, err := c.d.Transport.Observe(ctx, e.ID)
		if err != nil {
			return false, err
		}
		if obs.Nonce == e.Nonce && (obs.State == runner.ObsRunning || obs.State == runner.ObsCompleted) {
			if err := c.d.State.AcknowledgeEffect(ctx, e.ID, e.Nonce); err != nil {
				return false, err
			}
			continue
		}
		clear = false
	}
	return clear, nil
}

// processMailbox ingests the run's queued builder messages, at most
// maxMailboxPerTick, replaying any claimed before a crash first.
func (c *Controller) processMailbox(ctx context.Context, run state.Run, cfg runConfig) error {
	dir := RunDir(cfg.relay(), run.ID)
	var files []string
	for _, pattern := range []string{"*.json.taken", "*.json"} {
		m, _ := filepath.Glob(filepath.Join(dir, "q", pattern))
		sort.Strings(m)
		files = append(files, m...)
	}
	if len(files) > maxMailboxPerTick {
		files = files[:maxMailboxPerTick]
	}
	deps := c.mailboxDeps(run, cfg)
	var first error
	for _, f := range files {
		claimed := f
		if !strings.HasSuffix(f, ".taken") {
			claimed = f + ".taken"
			if err := os.Rename(f, claimed); err != nil {
				continue
			}
		}
		if _, err := ProcessClaimed(ctx, deps, claimed); err != nil {
			// Undecided: put it back so the next tick tries again.
			if _, serr := os.Stat(claimed); serr == nil {
				_ = os.Rename(claimed, strings.TrimSuffix(claimed, ".taken"))
			}
			// A builder still in its turn is waited for, not a failed tick.
			if first == nil && !errors.Is(err, ErrBuilderWorking) {
				first = err
			}
		}
	}
	return first
}

// ErrPlanChanged: the plan file is not the plan the run started with. A resume
// does not guess which tasks of the old plan are which in the new one; it stops
// until a person reconciles them.
var ErrPlanChanged = errors.New("the plan changed since the run started")

// checkPlan compares the plan file with the plan the run was started from. It
// runs when a run is resumed, not on every tick: a planner may tick boxes in the
// plan while it runs. A mismatch pauses a running run before anything is sent,
// and a run paused for it continues only once the same bytes are back.
func (c *Controller) checkPlan(ctx context.Context, run state.Run) error {
	if run.State == "stopping" {
		return nil // a stop is never held up by the plan
	}
	cfg, err := c.config(ctx, run)
	if err != nil {
		return err
	}
	raw, rerr := os.ReadFile(cfg.PlanPath)
	if rerr == nil && plan.Hash(string(raw)) == run.PlanHash {
		if run.State == "paused" && run.PauseCode == pausePlanChanged {
			return c.d.State.SetRunState(ctx, run.ID, "running", "")
		}
		return nil
	}
	if run.State == "running" {
		if err := c.d.State.SetRunState(ctx, run.ID, "paused", pausePlanChanged); err != nil {
			return err
		}
	}
	now := "cannot be read: " + fmt.Sprint(rerr)
	if rerr == nil {
		now = "now hashes to " + plan.Hash(string(raw))[:12]
	}
	return fmt.Errorf("%w: the plan at %s %s, but run %s started from %s. No prompt was sent. Restore the plan file to continue, or reconcile the run with its new plan explicitly: tasks are never matched by title or position",
		ErrPlanChanged, cfg.PlanPath, now, run.ID, run.PlanHash[:12])
}
