package workflow

import "fmt"

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidTransition, fmt.Sprintf(format, a...))
}

func incomplete(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrIncompleteEvidence, fmt.Sprintf(format, a...))
}

// Reduce applies one event to an attempt and says what changes and what work
// follows. It is pure: it reads only its arguments, performs no I/O and
// returns a new Attempt with Version advanced by one. The caller persists the
// result atomically; Reduce never sees a database, a clock or a process.
//
// Errors wrap ErrStaleAttempt (the attempt is no longer current),
// ErrInvalidTransition (the event does not fit the attempt's state or a
// precondition fails) or ErrIncompleteEvidence (a required fact is missing).
func Reduce(cur Attempt, ev Event, f Facts) (Transition, error) {
	if ev.Kind != EventStopRequested || ev.AttemptID != "" {
		if ev.AttemptID != cur.ID || ev.RunID != cur.RunID {
			return Transition{}, fmt.Errorf("%w: event %s names attempt %s of run %s, not %s of %s",
				ErrConflict, ev.ID, ev.AttemptID, ev.RunID, cur.ID, cur.RunID)
		}
	}
	known := false
	for _, k := range AllEventKinds {
		known = known || k == ev.Kind
	}
	if !known {
		return Transition{}, invalid("unknown event kind %q", ev.Kind)
	}
	if !cur.Current && ev.Kind != EventStopRequested {
		return Transition{}, fmt.Errorf("%w: attempt %s was superseded", ErrStaleAttempt, cur.ID)
	}

	next := cur
	next.Version = cur.Version + 1
	tr := Transition{Attempt: next}
	at := &tr.Attempt

	switch ev.Kind {
	case EventAttemptCreated:
		if cur.Version != 0 || cur.Implementation != ImplementationPending {
			return Transition{}, invalid("attempt %s was already created", cur.ID)
		}
		if !f.DependenciesAccepted {
			return Transition{}, invalid("a dependency of task %s is not accepted", cur.TaskID)
		}
		if !f.WriterOwned {
			return Transition{}, invalid("this process does not own the checkout writer")
		}
		if !f.SnapshotComplete {
			return Transition{}, incomplete("the start snapshot is missing or incomplete")
		}
		tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectBuilderDispatch, JobKind: "builder", InputHash: cur.PacketHash})

	case EventBuilderStarted:
		if !f.IdentityValid {
			return Transition{}, invalid("builder identity does not match the current attempt")
		}
		if cur.Implementation != ImplementationPending {
			return Transition{}, invalid("builder_started in implementation state %s", cur.Implementation)
		}
		at.Implementation = ImplementationRunning

	case EventBuilderStuck:
		if !f.IdentityValid {
			return Transition{}, invalid("builder identity does not match the current attempt")
		}
		if cur.Implementation != ImplementationPending && cur.Implementation != ImplementationRunning {
			return Transition{}, invalid("builder_stuck in implementation state %s", cur.Implementation)
		}
		at.Implementation = ImplementationStuck
		tr.PauseCode = PauseStuck

	case EventBuilderSubmitted:
		if !f.IdentityValid || !f.SessionSettled {
			return Transition{}, invalid("the submitting session is not the current, settled builder")
		}
		if cur.Implementation != ImplementationPending && cur.Implementation != ImplementationRunning {
			return Transition{}, invalid("builder_submitted in implementation state %s", cur.Implementation)
		}
		if !f.SnapshotComplete || !f.PatchComplete {
			return Transition{}, incomplete("the end snapshot or the patch is missing or incomplete")
		}
		at.Implementation = ImplementationSubmitted
		scheduleCheck(at, f, &tr)

	case EventCheckCompleted:
		if cur.Implementation != ImplementationSubmitted || (cur.Check != CheckPending && cur.Check != CheckRunning) {
			return Transition{}, invalid("check_completed with implementation %s and check %s", cur.Implementation, cur.Check)
		}
		if !f.CheckCertificateValid {
			return Transition{}, incomplete("no valid, complete check certificate for this attempt")
		}
		if !f.CheckPassed {
			at.Check = CheckFailed
			tr.PauseCode = PauseCheckFailed
			break
		}
		at.Check = CheckPassed
		scheduleReview(at, f, &tr)

	case EventReviewCompleted:
		if cur.Implementation != ImplementationSubmitted || (cur.Check != CheckPassed && cur.Check != CheckNotApplicable) ||
			(cur.Review != ReviewPending && cur.Review != ReviewRunning) {
			return Transition{}, invalid("review_completed with check %s and review %s", cur.Check, cur.Review)
		}
		if !f.ReviewResultValid {
			return Transition{}, incomplete("no valid, complete review result bound to this tree")
		}
		switch f.ReviewStatus {
		case ReviewPassed, ReviewChangesRequested:
			at.Review = f.ReviewStatus
		case ReviewFailed:
			at.Review = ReviewFailed
			tr.PauseCode = PauseReviewFailed
		default:
			return Transition{}, invalid("%q is not a reviewer outcome", f.ReviewStatus)
		}

	case EventRepairStarted:
		// A repair follows a builder review that asked for changes, or a valid
		// independent verification that did.
		verificationAsked := f.VerificationRequired && f.VerificationValid && f.VerificationStatus == ReviewChangesRequested
		if cur.Review != ReviewChangesRequested && !verificationAsked {
			return Transition{}, invalid("repair_started with review %s; only changes_requested permits a repair", cur.Review)
		}
		if !f.RepairAllowed {
			return Transition{}, invalid("no automatic repair attempts remain for task %s", cur.TaskID)
		}
		// The repair is a new attempt. This one stops being current, so later
		// events for it are stale; the controller creates the next one.
		at.Current = false
		tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectCreateRepairAttempt})

	case EventBudgetChanged:
		at.Budget = f.Budget
		// Only work the budget blocked is released: a submitted attempt whose
		// check was never enqueued. Nothing already running is touched.
		if f.SchedulingAllowed && cur.Implementation == ImplementationSubmitted && cur.Check == CheckPending {
			tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectCheckDispatch, JobKind: "check", InputHash: cur.EndSnapshotHash})
		}

	case EventAcceptRequested:
		if !Accepted(cur, f) {
			return Transition{}, incomplete("the attempt's persisted evidence does not satisfy acceptance")
		}
		ms := f.NowMS
		at.AcceptedMS = &ms
		tr.Accepted = true
		tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectAdvanceRun})

	case EventWaiveRequested:
		if !(f.ImplementationWaiver || f.CheckWaiver || f.ReviewWaiver) {
			return Transition{}, incomplete("no recorded waiver for this attempt")
		}
		if !f.WaiverTreeMatches {
			return Transition{}, incomplete("the waiver was recorded for a different tree than this attempt's evidence")
		}
		// Only the review requirement has a state of its own. A check or
		// implementation waiver stays a fact that Accepted consults, so the
		// recorded check result is never rewritten.
		if f.ReviewWaiver && cur.Review != ReviewPassed {
			at.Review = ReviewWaived
		}

	case EventStopRequested:
		tr.PauseCode = PauseStopping
		tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectCancel})
	}
	return tr, nil
}

// scheduleCheck decides what follows a submission.
func scheduleCheck(a *Attempt, f Facts, tr *Transition) {
	if f.NoCheckPolicy {
		a.Check = CheckNotApplicable
		scheduleReview(a, f, tr)
		return
	}
	a.Check = CheckPending
	if !f.SchedulingAllowed {
		tr.PauseCode = PauseScheduling
		return
	}
	tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectCheckDispatch, JobKind: "check", InputHash: a.EndSnapshotHash})
}

// scheduleReview decides what follows a passed or skipped check.
func scheduleReview(a *Attempt, f Facts, tr *Transition) {
	if f.NoReviewPolicy {
		a.Review = ReviewNotApplicable
		return
	}
	a.Review = ReviewPending
	if !f.SchedulingAllowed {
		tr.PauseCode = PauseScheduling
		return
	}
	tr.Effects = append(tr.Effects, EffectIntent{Kind: EffectReviewDispatch, JobKind: "reviewer", InputHash: a.PatchHash})
}

// Accepted reports whether an attempt's evidence satisfies acceptance: a
// submitted implementation (or waiver), a passed check or no-check policy (or
// waiver), a passed review or no-review policy or waiver for this tree, and
// complete snapshot and patch bindings. Budget is never considered.
func Accepted(a Attempt, f Facts) bool {
	return len(Unmet(a, f)) == 0
}

// Unmet names the acceptance requirements an attempt's persisted evidence does
// not satisfy, in order: "evidence" (no complete snapshot and patch),
// "implementation", "check", "review" and, when the run's policy requires it,
// "verification". Empty means Accepted.
func Unmet(a Attempt, f Facts) []string {
	if !f.SnapshotComplete || !f.PatchComplete {
		return []string{"evidence"}
	}
	implementation := a.Implementation == ImplementationSubmitted || (f.ImplementationWaiver && f.WaiverTreeMatches)
	check := (a.Check == CheckPassed && f.CheckCertificateValid && f.CheckPassed) ||
		(a.Check == CheckNotApplicable && f.NoCheckPolicy) ||
		(f.CheckWaiver && f.WaiverTreeMatches)
	review := (a.Review == ReviewPassed && f.ReviewResultValid) ||
		(a.Review == ReviewNotApplicable && f.NoReviewPolicy) ||
		(a.Review == ReviewWaived && f.ReviewWaiver && f.WaiverTreeMatches)
	verification := !f.VerificationRequired || (f.VerificationValid && f.VerificationStatus == ReviewPassed)
	var unmet []string
	for _, r := range []struct {
		name string
		ok   bool
	}{{"implementation", implementation}, {"check", check}, {"review", review}, {"verification", verification}} {
		if !r.ok {
			unmet = append(unmet, r.name)
		}
	}
	return unmet
}
