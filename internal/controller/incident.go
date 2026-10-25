package controller

// Recovery incidents: one failure, one decision.
//
// A failure the policy does not repair becomes an incident, keyed by the
// collaboration, the work (or task), the failed attempt and the failure kind.
// The row is written before the planner hears anything, so duplicates and
// replays find it and send nothing more. The planner is asked once, with the
// evidence, the spending and what is left.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// effectRecoveryDecision asks the planner what to do about an incident. It is
// sent while the run is paused.
const effectRecoveryDecision = "recovery_decision"

// failureClass is a failure as the policy sees it.
type failureClass struct {
	Kind     string
	Cause    string // the state-level cause when the kind can be repaired
	Concrete bool   // the failure names something specific to fix
	Evidence failureEvidence
	Why      string // why it is not repaired automatically, when it is not
}

// classifyFailure reads what the attempt itself shows. It never guesses: a
// failure it cannot read as one of the known kinds is unknown.
func (c *Controller) classifyFailure(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) failureClass {
	unknown := func(why string) failureClass { return failureClass{Kind: kindUnknown, Why: why} }
	switch {
	case att.Implementation == workflow.ImplementationStuck:
		ev, err := c.failureEvidence(ctx, run, att, state.CauseStuck)
		return failureClass{Kind: kindStuck, Evidence: ev, Why: errWhy(err, "the builder reported that it could not finish the task")}
	case att.Check == workflow.CheckFailed:
		ev, err := c.failureEvidence(ctx, run, att, state.CauseCheckFailure)
		if err != nil {
			return unknown("the declared check failed but its result is not complete evidence: " + err.Error())
		}
		return failureClass{Kind: kindDeclaredCheck, Cause: state.CauseCheckFailure, Evidence: ev, Concrete: cfg.CheckCommand != ""}
	case att.Review == workflow.ReviewChangesRequested:
		ev, err := c.failureEvidence(ctx, run, att, state.CauseReviewChanges)
		if err != nil {
			return unknown("the review asked for changes but its result is not readable evidence: " + err.Error())
		}
		concrete, err := c.hasConcreteFinding(ctx, att)
		if err != nil {
			return unknown(err.Error())
		}
		fc := failureClass{Kind: kindReviewChanges, Cause: state.CauseReviewChanges, Evidence: ev, Concrete: concrete}
		if !concrete {
			fc.Why = "the review asked for changes but names no open finding with a file to change"
		}
		return fc
	case att.Review == workflow.ReviewFailed:
		return failureClass{Kind: kindReviewFailed, Why: "the reviewer judged the work failed rather than asking for changes"}
	}
	return unknown("the attempt shows no failure the policy knows")
}

func errWhy(err error, fallback string) string {
	if err != nil {
		return fallback + " (its report could not be read: " + err.Error() + ")"
	}
	return fallback
}

// hasConcreteFinding: the latest review has at least one open finding that
// names a file.
func (c *Controller) hasConcreteFinding(ctx context.Context, att workflow.Attempt) (bool, error) {
	hash, found, err := c.d.State.LatestReviewResult(ctx, att.ID)
	if err != nil || !found {
		return false, err
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, hash, 1<<20)
	if err != nil {
		return false, err
	}
	var res workflow.ReviewResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return false, fmt.Errorf("%w: the review of attempt %s is unreadable", workflow.ErrCorruptArtifact, att.ID)
	}
	for _, f := range res.Findings {
		if f.Disposition == "open" && strings.TrimSpace(f.Path) != "" {
			return true, nil
		}
	}
	return false, nil
}

// incidentKey names one failure of one attempt of one piece of work.
func (c *Controller) incidentKey(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt, kind string) (key, workID string, err error) {
	workID, err = c.d.State.TaskWork(ctx, run.ID, att.TaskID)
	if err != nil {
		return "", "", err
	}
	subject := workID
	if subject == "" {
		subject = "task:" + run.ID + ":" + att.TaskID
	}
	return derive(cfg.Collaboration, subject, att.ID, kind), workID, nil
}

// raiseIncident records the incident and, once, asks the planner about it.
func (c *Controller) raiseIncident(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt, fc failureClass, why string) error {
	key, workID, err := c.incidentKey(ctx, run, cfg, att, fc.Kind)
	if err != nil {
		return err
	}
	inc, _, err := c.d.State.OpenIncident(ctx, state.IncidentSpec{Key: key, CollaborationID: cfg.Collaboration, RunID: run.ID, TaskID: att.TaskID,
		AttemptID: att.ID, LogicalWorkID: workID, Kind: fc.Kind, Reason: why, EvidenceHash: fc.Evidence.Hash})
	if err != nil {
		return err
	}
	if inc.State == "resolved" {
		return nil
	}
	parent, err := c.firstEffectOf(ctx, run, att)
	if err != nil {
		return err
	}
	text, err := c.decisionText(ctx, run, cfg, att, fc, why, key, workID)
	if err != nil {
		return err
	}
	payload, err := c.put(ctx, text)
	if err != nil {
		return err
	}
	// One decision per failed attempt: the effect is derived from the attempt's
	// own first effect, so asking again returns the one already queued.
	eff, err := c.d.State.InsertDerivedEffect(ctx, parent.ID, effectRecoveryDecision, payload)
	if err != nil {
		return err
	}
	return c.d.State.NoteIncidentNotified(ctx, key, eff.ID)
}

// firstEffectOf is the earliest effect an attempt caused.
func (c *Controller) firstEffectOf(ctx context.Context, run state.Run, att workflow.Attempt) (workflow.Effect, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return workflow.Effect{}, err
	}
	for _, e := range effects {
		if e.AttemptID == att.ID && e.Kind != effectRecoveryDecision {
			return e, nil
		}
	}
	return workflow.Effect{}, fmt.Errorf("%w: attempt %s has caused no effect to hang a decision on", workflow.ErrIncompleteEvidence, att.ID)
}

// decisionText is the one request the planner gets for an incident.
func (c *Controller) decisionText(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt, fc failureClass, why, key, workID string) (string, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[recovery] A decision is needed. Task %s of run %s stopped and was not repaired automatically.\n\n", att.TaskID, run.ID)
	fmt.Fprintf(&b, "Attempt %d (%s). Failure: %s. Not repaired automatically because: %s\n", att.Ordinal, att.ID, strings.ReplaceAll(fc.Kind, "_", " "), why)
	if fc.Evidence.Hash != "" {
		fmt.Fprintf(&b, "\nEvidence (artifact %s, read with `tandem artifact read %s --attempt %s`):\n%s\n", fc.Evidence.Hash, fc.Evidence.Hash, att.ID, fc.Evidence.Excerpt)
	}
	b.WriteString("\nAutomatic repairs used:\n")
	if workID == "" {
		b.WriteString("  none can be counted: this task is bound to no logical work, so it draws on no repair allowance\n")
	} else {
		budgets, err := c.d.State.WorkRepairBudgets(ctx, cfg.Collaboration, workID)
		if err != nil {
			return "", err
		}
		for _, g := range budgets {
			fmt.Fprintf(&b, "  budget group %s: %d of %d used\n", g.GroupID[:12], g.Used, g.Allowance)
		}
	}
	b.WriteString("\nProvider budgets of the collaboration:\n")
	providers, err := c.d.State.BudgetProviders(ctx, cfg.Collaboration)
	if err != nil {
		return "", err
	}
	if len(providers) == 0 {
		b.WriteString("  none declared\n")
	}
	for _, p := range providers {
		pb, err := c.d.State.ProviderBudget(ctx, cfg.Collaboration, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  %s: input %s\n", p, budgetLine(pb))
	}
	fmt.Fprintf(&b, "\nThe run has %d tasks; this is task %d. Nothing was replanned and no budget was raised.\n", len(tasks), taskOrdinal(tasks, att.TaskID))
	b.WriteString("\nChoose one. These are the only ways forward:\n")
	b.WriteString("  1. Fix it yourself in the checkout, then hand it back. Tandem checks and reviews the whole task again:\n")
	fmt.Fprintf(&b, "       tandem build reconcile --root %s --actor NAME --reason \"what you changed\"\n", cfg.Root)
	fmt.Fprintf(&b, "  2. Stop the run: tandem build stop --root %s\n", cfg.Root)
	b.WriteString("  3. To allow more spend, raise a budget explicitly with `tandem collaboration amend`. Resuming never raises one or resets a repair count.\n")
	fmt.Fprintf(&b, "\nIncident %s. It will not be raised again.\n", key)
	return b.String(), nil
}

func taskOrdinal(tasks []state.TaskRow, id string) int {
	for _, t := range tasks {
		if t.ID == id {
			return t.Ordinal
		}
	}
	return 0
}

func budgetLine(b state.ProviderBudget) string {
	if b.Unlimited {
		return fmt.Sprintf("%d spent, %d reserved, no ceiling", b.InputSpent, b.InputReserved)
	}
	if b.InputCeiling == nil {
		return fmt.Sprintf("%d spent, %d reserved, no ceiling declared", b.InputSpent, b.InputReserved)
	}
	return fmt.Sprintf("%d spent + %d reserved of %d", b.InputSpent, b.InputReserved, *b.InputCeiling)
}

// admitDecision reserves the planner turn a decision request is, against the
// planner's registered source. Without one the request waits; it is not sent
// uncounted.
func (c *Controller) admitDecision(ctx context.Context, run state.Run, cfg runConfig, eff workflow.Effect) error {
	if enforced, err := c.d.State.BudgetEnforced(ctx, cfg.Collaboration); err != nil || !enforced {
		return err // nothing is reserved, so no source is needed to count against
	}
	src, err := c.plannerSource(ctx, cfg)
	if err != nil {
		return err
	}
	return c.admitSourceScoped(ctx, run, cfg, eff.ID, eff.AttemptID, "", src.Provider, src.ID, false)
}
