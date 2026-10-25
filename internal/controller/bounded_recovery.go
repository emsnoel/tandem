package controller

// Bounded recovery: one automatic successor for a failed attempt.
//
// A successor is a new attempt in a fresh conversation, started from the
// task's original tree, so its final patch is the whole task's diff and both
// review stages and the check see all of it. The failure is carried as
// bounded evidence.
//
// The recovery policy decides whether to recover; this admits the successor
// once. The store consumes the allowance of every budget group the task draws
// on, or none, and a retry after a crash returns the same successor.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	pauseRecovery = "recovery_blocked"

	// recoveryExcerptBytes bounds the failure text in a successor's prompt.
	recoveryExcerptBytes = 2000
)

// failureEvidence is the structured evidence for a repair: its artifact and a
// bounded excerpt. It refuses a cause the attempt's own state does not show, so
// an unknown classification never becomes a repair.
type failureEvidence struct {
	Hash, Excerpt string
}

func (c *Controller) failureEvidence(ctx context.Context, run state.Run, parent workflow.Attempt, cause string) (failureEvidence, error) {
	notShown := func() error {
		return fmt.Errorf("%w: attempt %s does not show a %s failure", workflow.ErrIncompleteEvidence, parent.ID, cause)
	}
	switch cause {
	case state.CauseReviewChanges:
		if parent.Review != workflow.ReviewChangesRequested {
			return failureEvidence{}, notShown()
		}
		rc, err := c.repairFor(ctx, parent)
		if err != nil {
			return failureEvidence{}, err
		}
		for _, r := range rc.Refs {
			if r[0] == "parent_review" {
				return failureEvidence{Hash: r[1], Excerpt: boundExcerpt(rc.Note, true)}, nil
			}
		}
		return failureEvidence{}, notShown()
	case state.CauseCheckFailure:
		if parent.Check != workflow.CheckFailed {
			return failureEvidence{}, notShown()
		}
		cert, found, err := c.d.State.AttemptCheckCertificate(ctx, parent.ID)
		if err != nil {
			return failureEvidence{}, err
		}
		if !found || !cert.Complete || cert.ExitCode == 0 {
			return failureEvidence{}, notShown()
		}
		log, err := c.d.Artifacts.ReadAll(ctx, cert.LogHash, 64<<20)
		if err != nil {
			return failureEvidence{}, err
		}
		return failureEvidence{Hash: cert.LogHash, Excerpt: boundExcerpt(string(log), false)}, nil
	case state.CauseStuck:
		if parent.Implementation != workflow.ImplementationStuck {
			return failureEvidence{}, notShown()
		}
		events, err := c.d.State.RunEvents(ctx, run.ID)
		if err != nil {
			return failureEvidence{}, err
		}
		for i := len(events) - 1; i >= 0; i-- {
			if e := events[i]; e.AttemptID == parent.ID && e.Kind == workflow.EventBuilderStuck {
				raw, err := c.d.Artifacts.ReadAll(ctx, e.PayloadHash, 1<<20)
				if err != nil {
					return failureEvidence{}, err
				}
				return failureEvidence{Hash: e.PayloadHash, Excerpt: boundExcerpt(string(raw), true)}, nil
			}
		}
		return failureEvidence{}, notShown()
	}
	return failureEvidence{}, fmt.Errorf("%w: unknown repair cause %q", workflow.ErrInvalidTransition, cause)
}

// boundExcerpt cuts text to recoveryExcerptBytes on a rune boundary. A log's
// failure is at its end, so a log keeps its tail; other text keeps its head.
func boundExcerpt(text string, head bool) string {
	if len(text) <= recoveryExcerptBytes {
		return strings.TrimSpace(text)
	}
	if head {
		cut := recoveryExcerptBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		return strings.TrimSpace(text[:cut]) + "\n[cut]"
	}
	cut := len(text) - recoveryExcerptBytes
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return "[cut]\n" + strings.TrimSpace(text[cut:])
}

// taskBaseline is the start snapshot of the task's first attempt: the tree
// before any attempt touched the task.
func (c *Controller) taskBaseline(ctx context.Context, parent workflow.Attempt) (string, error) {
	attempts, err := c.d.State.TaskAttempts(ctx, parent.RunID, parent.TaskID)
	if err != nil {
		return "", err
	}
	for _, a := range attempts {
		if a.ParentAttemptID == "" && a.StartSnapshotHash != "" {
			return a.StartSnapshotHash, nil
		}
	}
	return "", fmt.Errorf("%w: task %s has no first attempt with a start snapshot", workflow.ErrIncompleteEvidence, parent.TaskID)
}

// changedSince lists the paths whose content differs between two trees.
func (c *Controller) changedSince(ctx context.Context, from, to string) ([]string, error) {
	a, err := snapshot.LoadManifest(ctx, c.d.Artifacts, from)
	if err != nil {
		return nil, err
	}
	b, err := snapshot.LoadManifest(ctx, c.d.Artifacts, to)
	if err != nil {
		return nil, err
	}
	patch, err := snapshot.Diff(a, b)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ch := range patch.Changes {
		out = append(out, ch.Path)
	}
	return out, nil
}

// successorNote is what the successor's prompt opens with.
func successorNote(parent workflow.Attempt, cause string, ev failureEvidence, baseline, current string, retained []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This is attempt %d, the one automatic recovery of attempt %d of this task (%s). It is a fresh conversation. The work attempt %d left is still in the tree; finish the whole task from there.",
		parent.Ordinal+1, parent.Ordinal, strings.ReplaceAll(cause, "_", " "), parent.Ordinal)
	fmt.Fprintf(&b, "\n\nThe task started from tree %s. The failure left tree %s.", baseline[:12], current[:12])
	if len(retained) > 0 {
		shown, more := retained, 0
		if len(shown) > repairPaths {
			shown, more = shown[:repairPaths], len(shown)-repairPaths
		}
		fmt.Fprintf(&b, "\nFiles changed since the task started: %s", strings.Join(shown, ", "))
		if more > 0 {
			fmt.Fprintf(&b, " and %d more", more)
		}
		b.WriteString(".")
	}
	fmt.Fprintf(&b, "\n\nWhat went wrong (bounded; the whole of it is `tandem artifact read %s --attempt ATTEMPT`, where ATTEMPT is in the Context line):\n%s", ev.Hash, ev.Excerpt)
	return b.String()
}

// admitSuccessor admits and starts the one automatic successor of parent.
// Calling it again for the same parent returns the successor it already made.
func (c *Controller) admitSuccessor(ctx context.Context, run state.Run, cfg runConfig, parent workflow.Attempt, cause string) (workflow.Attempt, error) {
	if cfg.Collaboration == "" {
		return workflow.Attempt{}, fmt.Errorf("%w: bounded recovery draws on a collaboration's budget groups, and this run has none", workflow.ErrUnsupported)
	}
	if prior, found, err := c.d.State.RepairAdmissionFor(ctx, parent.ID); err != nil {
		return workflow.Attempt{}, err
	} else if found {
		return c.resumeSuccessor(ctx, run, cfg, prior.SuccessorID)
	}
	if !parent.Current {
		return workflow.Attempt{}, fmt.Errorf("%w: attempt %s is not the current attempt", workflow.ErrStaleAttempt, parent.ID)
	}
	if err := c.writerSettled(ctx, run, parent); err != nil {
		return workflow.Attempt{}, err
	}
	ev, err := c.failureEvidence(ctx, run, parent, cause)
	if err != nil {
		return workflow.Attempt{}, err
	}
	baseline, err := c.taskBaseline(ctx, parent)
	if err != nil {
		return workflow.Attempt{}, err
	}
	current, err := c.captureStart(ctx, cfg)
	if err != nil {
		return workflow.Attempt{}, err
	}
	retained, err := c.changedSince(ctx, baseline, current)
	if err != nil {
		return workflow.Attempt{}, err
	}
	task, err := c.taskOf(ctx, run, parent)
	if err != nil {
		return workflow.Attempt{}, err
	}
	requestID := derive(parent.ID, "bounded-repair")
	spec, err := c.prepareAttempt(ctx, run, cfg, task, derive(requestID, "successor"), parent.Ordinal+1, parent.ID, baseline,
		repairContext{Note: successorNote(parent, cause, ev, baseline, current, retained),
			Refs: [][2]string{{"failure_evidence", ev.Hash}, {"task_baseline", baseline}, {"failed_tree", current}}})
	if err != nil {
		return workflow.Attempt{}, err
	}
	fresh, err := c.d.State.GetRun(ctx, run.ID)
	if err != nil {
		return workflow.Attempt{}, err
	}
	gen, err := c.d.State.StopGeneration(ctx, cfg.Collaboration)
	if err != nil {
		return workflow.Attempt{}, err
	}
	adm, err := c.d.State.AdmitRepair(ctx, state.RecoveryRequest{ID: requestID, CollaborationID: cfg.Collaboration, RunID: run.ID, TaskID: task.ID,
		ParentAttemptID: parent.ID, ParentVersion: parent.Version, Cause: cause, EvidenceHash: ev.Hash, OriginalTreeHash: baseline,
		CurrentTreeHash: current, RunVersion: fresh.Version, StopGeneration: gen, LeaseFence: c.fence(run.CheckoutID), Attempt: spec})
	switch {
	case errors.Is(err, state.ErrWorkMapping), errors.Is(err, state.ErrRepairExhausted):
		// Which allowance this draws on is unknown, or it is spent: a person decides.
		return workflow.Attempt{}, errors.Join(err, c.d.State.SetRunState(ctx, run.ID, "paused", pauseRecovery))
	case err != nil:
		return workflow.Attempt{}, err
	}
	return c.resumeSuccessor(ctx, fresh, cfg, adm.SuccessorID)
}

// resumeSuccessor starts an admitted successor and lets a run that was paused
// by the failure carry on. Both steps are idempotent, so a crash between the
// admission and here is finished by calling it again.
func (c *Controller) resumeSuccessor(ctx context.Context, run state.Run, cfg runConfig, successorID string) (workflow.Attempt, error) {
	if err := c.afterAttemptCreated(ctx, run, cfg, successorID); err != nil {
		return workflow.Attempt{}, err
	}
	if cur, err := c.d.State.GetRun(ctx, run.ID); err == nil && cur.State == "paused" &&
		(cur.PauseCode == workflow.PauseStuck || cur.PauseCode == workflow.PauseCheckFailed || cur.PauseCode == workflow.PauseReviewFailed) {
		if err := c.d.State.SetRunState(ctx, run.ID, "running", ""); err != nil {
			return workflow.Attempt{}, err
		}
	}
	return c.d.State.GetAttempt(ctx, successorID)
}

// writerSettled: the failed attempt's builder is not writing any more. A tree
// captured while it may still write is a guess.
func (c *Controller) writerSettled(ctx context.Context, run state.Run, parent workflow.Attempt) error {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return err
	}
	// An attempt a person handed over (a reconcile) had no builder of its own:
	// the writer to ask about is the one of the attempt it replaced.
	owner := parent.ID
	for hops := 0; hops < state.MaxAttempts+1; hops++ {
		if from, ok, err := c.d.State.ReconcileInto(ctx, owner); err != nil {
			return err
		} else if ok {
			owner = from
		} else {
			break
		}
	}
	for _, e := range effects {
		if e.Kind != workflow.EffectBuilderDispatch || e.AttemptID != owner {
			continue
		}
		obs, err := c.d.Transport.Observe(ctx, e.ID)
		if err != nil {
			return fmt.Errorf("%w: the builder cannot be observed: %v", workflow.ErrBusy, err)
		}
		if obs.State != runner.ObsCompleted {
			return fmt.Errorf("%w: the builder of attempt %s is %q, so the tree is not known", workflow.ErrBusy, parent.ID, obs.State)
		}
		return nil
	}
	return fmt.Errorf("%w: attempt %s has no builder dispatch to observe", workflow.ErrIncompleteEvidence, parent.ID)
}
