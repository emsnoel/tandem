package workflow

import (
	"errors"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func fresh() Attempt {
	return Attempt{
		ID: "a1", RunID: "r1", TaskID: "t1", Ordinal: 1, Current: true,
		Implementation: ImplementationPending, Check: CheckPending, Review: ReviewPending, Budget: BudgetAvailable,
		StartSnapshotHash: h("start"), PacketHash: h("packet"),
	}
}

func h(s string) string { return strings.Repeat(string(rune('a'+len(s)%6)), 64) }

func ev(kind string) Event {
	return Event{ID: "e1", RunID: "r1", AttemptID: "a1", Kind: kind, PayloadHash: h("payload")}
}

func submitted() Attempt {
	a := fresh()
	a.Implementation, a.EndSnapshotHash, a.PatchHash = ImplementationSubmitted, h("end"), h("patch")
	return a
}

func okFacts() Facts {
	return Facts{
		IdentityValid: true, SessionSettled: true, SnapshotComplete: true, PatchComplete: true,
		SchedulingAllowed: true, DependenciesAccepted: true, WriterOwned: true, RepairAllowed: true,
		Budget: BudgetAvailable, NowMS: 1000,
	}
}

func reduce(t *testing.T, a Attempt, kind string, f Facts) Transition {
	t.Helper()
	tr, err := Reduce(a, ev(kind), f)
	if err != nil {
		t.Fatalf("Reduce(%s): %v", kind, err)
	}
	return tr
}

func refuses(t *testing.T, a Attempt, kind string, f Facts, want error) {
	t.Helper()
	tr, err := Reduce(a, ev(kind), f)
	if !errors.Is(err, want) {
		t.Fatalf("Reduce(%s) = %+v, %v; want %v", kind, tr, err, want)
	}
}

func kinds(t Transition) []string {
	var out []string
	for _, e := range t.Effects {
		out = append(out, e.Kind)
	}
	return out
}

func TestReduceBumpsTheVersionAndNeverMutatesItsInput(t *testing.T) {
	a := fresh()
	tr := reduce(t, a, EventAttemptCreated, okFacts())
	if tr.Attempt.Version != 1 || a.Version != 0 {
		t.Fatalf("versions: next %d, input %d", tr.Attempt.Version, a.Version)
	}
}

func TestAttemptCreatedEnqueuesTheBuilderOnce(t *testing.T) {
	tr := reduce(t, fresh(), EventAttemptCreated, okFacts())
	if got := kinds(tr); len(got) != 1 || got[0] != EffectBuilderDispatch || tr.Effects[0].InputHash != h("packet") {
		t.Fatalf("effects %+v", tr.Effects)
	}
	if tr.Attempt.Implementation != ImplementationPending {
		t.Fatalf("implementation %s: it is pending until the builder acknowledges", tr.Attempt.Implementation)
	}
	for name, mutate := range map[string]func(*Facts){
		"dependencies not accepted": func(f *Facts) { f.DependenciesAccepted = false },
		"writer not owned":          func(f *Facts) { f.WriterOwned = false },
	} {
		f := okFacts()
		mutate(&f)
		refuses(t, fresh(), EventAttemptCreated, f, ErrInvalidTransition)
		_ = name
	}
	f := okFacts()
	f.SnapshotComplete = false
	refuses(t, fresh(), EventAttemptCreated, f, ErrIncompleteEvidence)
	again := fresh()
	again.Version = 1
	refuses(t, again, EventAttemptCreated, okFacts(), ErrInvalidTransition)
}

func TestBuilderStartedAndStuck(t *testing.T) {
	a := fresh()
	a.Version = 1
	if got := reduce(t, a, EventBuilderStarted, okFacts()).Attempt.Implementation; got != ImplementationRunning {
		t.Fatalf("started: %s", got)
	}
	f := okFacts()
	f.IdentityValid = false
	refuses(t, a, EventBuilderStarted, f, ErrInvalidTransition)

	tr := reduce(t, a, EventBuilderStuck, okFacts())
	if tr.Attempt.Implementation != ImplementationStuck || tr.PauseCode != PauseStuck {
		t.Fatalf("stuck: %+v", tr)
	}
	done := submitted()
	refuses(t, done, EventBuilderStarted, okFacts(), ErrInvalidTransition)
}

func TestBuilderSubmittedSchedulesTheCheck(t *testing.T) {
	a := fresh()
	a.Implementation, a.EndSnapshotHash, a.PatchHash = ImplementationRunning, h("end"), h("patch")
	tr := reduce(t, a, EventBuilderSubmitted, okFacts())
	if tr.Attempt.Implementation != ImplementationSubmitted || tr.Attempt.Check != CheckPending {
		t.Fatalf("attempt %+v", tr.Attempt)
	}
	if got := kinds(tr); len(got) != 1 || got[0] != EffectCheckDispatch || tr.Effects[0].InputHash != h("end") {
		t.Fatalf("effects %+v", tr.Effects)
	}

	blocked := okFacts()
	blocked.SchedulingAllowed = false
	tr = reduce(t, a, EventBuilderSubmitted, blocked)
	if len(tr.Effects) != 0 || tr.PauseCode != PauseScheduling {
		t.Fatalf("a blocked scheduler still enqueued work or did not pause: %+v", tr)
	}

	noCheck := okFacts()
	noCheck.NoCheckPolicy = true
	tr = reduce(t, a, EventBuilderSubmitted, noCheck)
	if tr.Attempt.Check != CheckNotApplicable || kinds(tr)[0] != EffectReviewDispatch {
		t.Fatalf("no-check policy: %+v", tr)
	}

	for name, f := range map[string]Facts{
		"session not settled": func() Facts { f := okFacts(); f.SessionSettled = false; return f }(),
		"identity invalid":    func() Facts { f := okFacts(); f.IdentityValid = false; return f }(),
	} {
		if _, err := Reduce(a, ev(EventBuilderSubmitted), f); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, f := range map[string]Facts{
		"snapshot incomplete": func() Facts { f := okFacts(); f.SnapshotComplete = false; return f }(),
		"patch incomplete":    func() Facts { f := okFacts(); f.PatchComplete = false; return f }(),
	} {
		if _, err := Reduce(a, ev(EventBuilderSubmitted), f); !errors.Is(err, ErrIncompleteEvidence) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCheckCompleted(t *testing.T) {
	a := submitted()
	a.Check = CheckRunning
	valid := okFacts()
	valid.CheckCertificateValid, valid.CheckPassed = true, true

	tr := reduce(t, a, EventCheckCompleted, valid)
	if tr.Attempt.Check != CheckPassed || kinds(tr)[0] != EffectReviewDispatch || tr.Effects[0].InputHash != h("patch") {
		t.Fatalf("passed: %+v", tr)
	}
	noReview := valid
	noReview.NoReviewPolicy = true
	if tr = reduce(t, a, EventCheckCompleted, noReview); tr.Attempt.Review != ReviewNotApplicable || len(tr.Effects) != 0 {
		t.Fatalf("no-review policy: %+v", tr)
	}
	failed := okFacts()
	failed.CheckCertificateValid = true
	if tr = reduce(t, a, EventCheckCompleted, failed); tr.Attempt.Check != CheckFailed || tr.PauseCode != PauseCheckFailed || len(tr.Effects) != 0 {
		t.Fatalf("failed: %+v", tr)
	}
	// Without a valid certificate there is no check result at all, whatever the event says.
	refuses(t, a, EventCheckCompleted, okFacts(), ErrIncompleteEvidence)
	notSubmitted := fresh()
	refuses(t, notSubmitted, EventCheckCompleted, valid, ErrInvalidTransition)
}

func TestReviewCompleted(t *testing.T) {
	a := submitted()
	a.Check, a.Review = CheckPassed, ReviewRunning
	for status, want := range map[ReviewState]ReviewState{
		ReviewPassed: ReviewPassed, ReviewChangesRequested: ReviewChangesRequested, ReviewFailed: ReviewFailed,
	} {
		f := okFacts()
		f.ReviewResultValid, f.ReviewStatus = true, status
		tr := reduce(t, a, EventReviewCompleted, f)
		if tr.Attempt.Review != want {
			t.Errorf("%s: review %s", status, tr.Attempt.Review)
		}
	}
	f := okFacts()
	f.ReviewStatus = ReviewPassed // a status with no valid result behind it
	refuses(t, a, EventReviewCompleted, f, ErrIncompleteEvidence)
	f.ReviewResultValid, f.ReviewStatus = true, ReviewWaived // not a reviewer outcome
	refuses(t, a, EventReviewCompleted, f, ErrInvalidTransition)
	earlier := submitted()
	f.ReviewStatus = ReviewPassed
	refuses(t, earlier, EventReviewCompleted, f, ErrInvalidTransition)
}

func TestRepairStartedSupersedesTheAttempt(t *testing.T) {
	a := submitted()
	a.Check, a.Review = CheckPassed, ReviewChangesRequested
	tr := reduce(t, a, EventRepairStarted, okFacts())
	if tr.Attempt.Current || kinds(tr)[0] != EffectCreateRepairAttempt {
		t.Fatalf("repair: %+v", tr)
	}
	f := okFacts()
	f.RepairAllowed = false
	refuses(t, a, EventRepairStarted, f, ErrInvalidTransition)
	passed := submitted()
	passed.Check, passed.Review = CheckPassed, ReviewPassed
	refuses(t, passed, EventRepairStarted, okFacts(), ErrInvalidTransition)
}

func TestAcceptRequiresTheTrustedFacts(t *testing.T) {
	a := submitted()
	a.Check, a.Review = CheckPassed, ReviewPassed
	good := okFacts()
	good.CheckCertificateValid, good.CheckPassed, good.ReviewResultValid = true, true, true
	tr := reduce(t, a, EventAcceptRequested, good)
	if !tr.Accepted || tr.Attempt.AcceptedMS == nil || *tr.Attempt.AcceptedMS != 1000 || kinds(tr)[0] != EffectAdvanceRun {
		t.Fatalf("accept: %+v", tr)
	}
	bad := good
	bad.CheckCertificateValid = false
	refuses(t, a, EventAcceptRequested, bad, ErrIncompleteEvidence)
}

func TestAcceptedTruthTable(t *testing.T) {
	pass := func(a *Attempt) { a.Check, a.Review = CheckPassed, ReviewPassed }
	cases := []struct {
		name    string
		attempt func() Attempt
		facts   func(*Facts)
		want    bool
	}{
		{"everything valid", func() Attempt { a := submitted(); pass(&a); return a }, func(f *Facts) {}, true},
		{"implementation not submitted", func() Attempt { a := fresh(); pass(&a); return a }, func(f *Facts) {}, false},
		{"check not passed", func() Attempt { a := submitted(); pass(&a); a.Check = CheckFailed; return a }, func(f *Facts) {}, false},
		{"check passed without a certificate", func() Attempt { a := submitted(); pass(&a); return a }, func(f *Facts) { f.CheckCertificateValid = false }, false},
		{"review passed without a valid result", func() Attempt { a := submitted(); pass(&a); return a }, func(f *Facts) { f.ReviewResultValid = false }, false},
		{"review changes requested", func() Attempt { a := submitted(); pass(&a); a.Review = ReviewChangesRequested; return a }, func(f *Facts) {}, false},
		{"snapshot incomplete", func() Attempt { a := submitted(); pass(&a); return a }, func(f *Facts) { f.SnapshotComplete = false }, false},
		{"patch incomplete", func() Attempt { a := submitted(); pass(&a); return a }, func(f *Facts) { f.PatchComplete = false }, false},
		{"no-check policy", func() Attempt { a := submitted(); pass(&a); a.Check = CheckNotApplicable; return a }, func(f *Facts) { f.CheckCertificateValid, f.NoCheckPolicy = false, true }, true},
		{"not-applicable check without the policy", func() Attempt { a := submitted(); pass(&a); a.Check = CheckNotApplicable; return a }, func(f *Facts) { f.CheckCertificateValid = false }, false},
		{"no-review policy", func() Attempt { a := submitted(); pass(&a); a.Review = ReviewNotApplicable; return a }, func(f *Facts) { f.ReviewResultValid, f.NoReviewPolicy = false, true }, true},
		{"review waived for this tree", func() Attempt { a := submitted(); pass(&a); a.Review = ReviewWaived; return a }, func(f *Facts) { f.ReviewResultValid, f.ReviewWaiver, f.WaiverTreeMatches = false, true, true }, true},
		{"review waived for another tree", func() Attempt { a := submitted(); pass(&a); a.Review = ReviewWaived; return a }, func(f *Facts) { f.ReviewResultValid, f.ReviewWaiver = false, true }, false},
		{"failed check waived", func() Attempt { a := submitted(); pass(&a); a.Check = CheckFailed; return a }, func(f *Facts) { f.CheckWaiver, f.WaiverTreeMatches = true, true }, true},
		{"stuck implementation waived", func() Attempt { a := fresh(); pass(&a); a.Implementation = ImplementationStuck; return a }, func(f *Facts) { f.ImplementationWaiver, f.WaiverTreeMatches = true, true }, true},
		{"budget exhausted does not matter", func() Attempt { a := submitted(); pass(&a); a.Budget = BudgetExhausted; return a }, func(f *Facts) { f.Budget = BudgetExhausted; f.SchedulingAllowed = false }, true},
	}
	for _, c := range cases {
		f := okFacts()
		f.CheckCertificateValid, f.CheckPassed, f.ReviewResultValid = true, true, true
		c.facts(&f)
		if got := Accepted(c.attempt(), f); got != c.want {
			t.Errorf("%s: Accepted = %v, want %v", c.name, got, c.want)
		}
		if unmet := Unmet(c.attempt(), f); (len(unmet) == 0) != c.want {
			t.Errorf("%s: Unmet = %v disagrees with Accepted = %v", c.name, unmet, c.want)
		}
	}
}

func TestUnmetNamesEachRequirementThatFails(t *testing.T) {
	f := okFacts()
	f.CheckCertificateValid, f.CheckPassed, f.ReviewResultValid = true, true, true
	a := submitted()
	a.Check, a.Review = CheckFailed, ReviewPending
	if got := Unmet(a, f); len(got) != 2 || got[0] != "check" || got[1] != "review" {
		t.Fatalf("a failed check and a pending review: %v", got)
	}
	f.PatchComplete = false
	a.Check, a.Review = CheckPassed, ReviewPassed
	if got := Unmet(a, f); len(got) != 1 || got[0] != "evidence" {
		t.Fatalf("an attempt with no patch: %v", got)
	}
	if got := Unmet(fresh(), okFacts()); len(got) == 0 || got[0] != "implementation" {
		t.Fatalf("an attempt never submitted: %v", got)
	}
}

func TestWaiveRequested(t *testing.T) {
	a := submitted()
	a.Check, a.Review = CheckPassed, ReviewChangesRequested
	f := okFacts()
	f.ReviewWaiver, f.WaiverTreeMatches = true, true
	if tr := reduce(t, a, EventWaiveRequested, f); tr.Attempt.Review != ReviewWaived {
		t.Fatalf("review %s", tr.Attempt.Review)
	}
	f.WaiverTreeMatches = false
	refuses(t, a, EventWaiveRequested, f, ErrIncompleteEvidence)
	refuses(t, a, EventWaiveRequested, okFacts(), ErrIncompleteEvidence)
	// A check waiver is a fact for Accepted, not a state: the check stays failed.
	failed := submitted()
	failed.Check = CheckFailed
	cw := okFacts()
	cw.CheckWaiver, cw.WaiverTreeMatches = true, true
	if tr := reduce(t, failed, EventWaiveRequested, cw); tr.Attempt.Check != CheckFailed {
		t.Fatalf("a waiver rewrote the check result to %s", tr.Attempt.Check)
	}
}

func TestStopRequestedCancels(t *testing.T) {
	tr := reduce(t, submitted(), EventStopRequested, okFacts())
	if tr.PauseCode != PauseStopping || kinds(tr)[0] != EffectCancel {
		t.Fatalf("stop: %+v", tr)
	}
}

func TestBudgetChangedReleasesBlockedChecksOnly(t *testing.T) {
	blocked := submitted() // check pending but never enqueued
	tr := reduce(t, blocked, EventBudgetChanged, okFacts())
	if len(tr.Effects) != 1 || tr.Effects[0].Kind != EffectCheckDispatch {
		t.Fatalf("a freed budget did not schedule the blocked check: %+v", tr)
	}
	f := okFacts()
	f.SchedulingAllowed, f.Budget = false, BudgetExhausted
	tr = reduce(t, blocked, EventBudgetChanged, f)
	if len(tr.Effects) != 0 || tr.Attempt.Budget != BudgetExhausted {
		t.Fatalf("still exhausted: %+v", tr)
	}
	running := fresh()
	running.Implementation = ImplementationRunning
	if tr = reduce(t, running, EventBudgetChanged, okFacts()); len(tr.Effects) != 0 {
		t.Fatalf("a running builder was rescheduled: %+v", tr)
	}
}

func TestStaleAndUnknownEventsAreRefused(t *testing.T) {
	old := submitted()
	old.Current = false
	for _, kind := range AllEventKinds {
		if kind == EventStopRequested {
			continue
		}
		refuses(t, old, kind, okFacts(), ErrStaleAttempt)
	}
	refuses(t, fresh(), "made_up_event", okFacts(), ErrInvalidTransition)
	other := ev(EventBuilderStarted)
	other.AttemptID = "someone-else"
	if _, err := Reduce(fresh(), other, okFacts()); !errors.Is(err, ErrConflict) {
		t.Fatalf("an event for another attempt: %v", err)
	}
}

func TestEveryDeclaredEventKindIsHandled(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range AllEventKinds {
		if seen[k] {
			t.Errorf("%s is declared twice", k)
		}
		seen[k] = true
		if _, err := Reduce(fresh(), ev(k), Facts{}); err != nil && strings.Contains(err.Error(), "unknown event") {
			t.Errorf("%s has no case in Reduce", k)
		}
	}
	if len(AllEventKinds) != 11 {
		t.Fatalf("%d event kinds; C3 lists 11", len(AllEventKinds))
	}
}

// The reducer must stay pure so its transitions can be tested without a
// database, a process or a worktree, and so nothing effectful hides in it.
func TestWorkflowPackageStaysPure(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	forbidden := []string{"/internal/state", "/internal/runner", "/internal/vcs", "/internal/artifact", "database/sql", "os/exec", `"os"`, "net/http", `"time"`}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			for _, bad := range forbidden {
				if strings.Contains(imp.Path.Value, bad) {
					t.Errorf("%s imports %s", f, imp.Path.Value)
				}
			}
		}
	}
}
