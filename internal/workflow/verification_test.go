package workflow

import "testing"

// reviewed is a submitted attempt whose check and builder review passed.
func reviewed() (Attempt, Facts) {
	a := submitted()
	a.Check, a.Review = CheckPassed, ReviewPassed
	f := okFacts()
	f.CheckCertificateValid, f.CheckPassed, f.ReviewResultValid, f.ReviewStatus = true, true, true, ReviewPassed
	return a, f
}

func TestAcceptanceWithoutAVerificationPolicyIsUnchanged(t *testing.T) {
	a, f := reviewed()
	if !Accepted(a, f) {
		t.Fatalf("a run that did not opt in lost acceptance: %v", Unmet(a, f))
	}
}

func TestAcceptanceNeedsIndependentVerificationWhenRequired(t *testing.T) {
	a, f := reviewed()
	f.VerificationRequired = true
	if got := Unmet(a, f); len(got) != 1 || got[0] != "verification" {
		t.Fatalf("builder review passed but nobody verified: unmet %v", got)
	}
	refuses(t, a, EventAcceptRequested, f, ErrIncompleteEvidence)

	for name, tc := range map[string]struct {
		valid  bool
		status ReviewState
	}{
		"a pass from a reviewer who is not distinct, or for another tree": {false, ReviewPassed},
		"a valid request for changes":                                     {true, ReviewChangesRequested},
		"a valid failure":                                                 {true, ReviewFailed},
		"a result with no status":                                         {true, ""},
	} {
		g := f
		g.VerificationValid, g.VerificationStatus = tc.valid, tc.status
		if Accepted(a, g) {
			t.Errorf("%s: accepted", name)
		}
	}
	f.VerificationValid, f.VerificationStatus = true, ReviewPassed
	if !Accepted(a, f) {
		t.Fatalf("every gate met but unmet = %v", Unmet(a, f))
	}
	tr := reduce(t, a, EventAcceptRequested, f)
	if !tr.Accepted {
		t.Fatal("the accept event did not accept")
	}
}

// A waiver of the review requirement never satisfies verification: verification
// has no waiver.
func TestAcceptanceVerificationCannotBeWaived(t *testing.T) {
	a, f := reviewed()
	a.Review = ReviewWaived
	f.ReviewStatus, f.ReviewResultValid = "", false
	f.ReviewWaiver, f.WaiverTreeMatches = true, true
	f.VerificationRequired = true
	if Accepted(a, f) {
		t.Fatalf("a review waiver stood in for verification: unmet %v", Unmet(a, f))
	}
	f.CheckWaiver, f.ImplementationWaiver = true, true
	if Accepted(a, f) {
		t.Fatal("the other waivers stood in for verification")
	}
}

func TestAcceptanceVerificationAndEvidenceKeepTheirOrder(t *testing.T) {
	a, f := reviewed()
	f.VerificationRequired = true
	a.Check = CheckPending
	f.CheckCertificateValid = false
	a.Review = ReviewPending
	f.ReviewResultValid = false
	want := []string{"check", "review", "verification"}
	got := Unmet(a, f)
	if len(got) != len(want) {
		t.Fatalf("unmet %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unmet %v, want %v", got, want)
		}
	}
	f.SnapshotComplete = false
	if got := Unmet(a, f); len(got) != 1 || got[0] != "evidence" {
		t.Fatalf("without evidence: %v", got)
	}
}

// Verification that asks for changes sends the attempt to repair, the way a
// builder review that asks for changes does.
func TestRepairFollowsAVerificationThatRequestsChanges(t *testing.T) {
	a, f := reviewed()
	f.VerificationRequired = true
	refuses(t, a, EventRepairStarted, f, ErrInvalidTransition)
	f.VerificationValid, f.VerificationStatus = true, ReviewChangesRequested
	tr := reduce(t, a, EventRepairStarted, f)
	if tr.Attempt.Current || len(tr.Effects) != 1 || tr.Effects[0].Kind != EffectCreateRepairAttempt {
		t.Fatalf("repair transition %+v", tr)
	}
	f.RepairAllowed = false
	refuses(t, a, EventRepairStarted, f, ErrInvalidTransition)
	// A verification that is not valid does not send anything to repair.
	g := f
	g.RepairAllowed, g.VerificationValid = true, false
	refuses(t, a, EventRepairStarted, g, ErrInvalidTransition)
	// Nor does a policy that was never required.
	h := f
	h.RepairAllowed, h.VerificationRequired = true, false
	refuses(t, a, EventRepairStarted, h, ErrInvalidTransition)
}
