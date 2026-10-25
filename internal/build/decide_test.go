package build

import "testing"

func exit(n int) *int { return &n }

func TestDecide(t *testing.T) {
	over := &Usage{Input: 4000000}
	under := &Usage{Input: 1000000}
	for _, c := range []struct {
		name       string
		in         Outcome
		wantStatus string
		wantOut    string
	}{
		{"stuck beats everything", Decide(true, exit(1), over, 3000000, false), StatusStuck, "stop:stuck"},
		{"a red check stops", Decide(false, exit(2), under, 3000000, false), StatusRed, "stop:red"},
		{"a red last task stops", Decide(false, exit(2), under, 3000000, true), StatusRed, "stop:red"},
		{"over budget stops but the task is done", Decide(false, exit(0), over, 3000000, false), StatusDone, "stop:budget"},
		{"over budget on the last task finishes", Decide(false, exit(0), over, 3000000, true), StatusDone, "finish"},
		{"budget 0 is off", Decide(false, exit(0), over, 0, false), StatusDone, "next"},
		{"unmeasured usage is not over budget", Decide(false, exit(0), nil, 3000000, false), StatusDone, "next"},
		{"no check command still moves on", Decide(false, nil, under, 3000000, false), StatusDone, "next"},
		{"the last task finishes", Decide(false, exit(0), under, 3000000, true), StatusDone, "finish"},
	} {
		if got := c.in.Status; got != c.wantStatus {
			t.Errorf("%s: status %q, want %q", c.name, got, c.wantStatus)
		}
		if got := c.in.String(); got != c.wantOut {
			t.Errorf("%s: outcome %q, want %q", c.name, got, c.wantOut)
		}
	}
}

func advanceFacts(f AdvanceFacts) AdvanceFacts {
	if f.Budget == 0 {
		f.Budget = 100
	}
	return f
}

func TestDecideAdvancePendingReviewOverBudget(t *testing.T) {
	for _, c := range []struct {
		name  string
		facts AdvanceFacts
	}{
		{"exactly at the limit", AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 100}}},
		{"over the limit", AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 101}}},
	} {
		got := DecideAdvance(advanceFacts(c.facts))
		if got.Status != StatusReviewing || got.String() != "stop:budget" {
			t.Errorf("%s: %+v, want reviewing stop:budget", c.name, got)
		}
	}
}

func TestDecideAdvanceReviewWithinBudgetAsksForReview(t *testing.T) {
	got := DecideAdvance(advanceFacts(AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 99}}))
	if got.Status != StatusReviewing || got.Next != "review" {
		t.Fatalf("%+v, want reviewing review", got)
	}
}

func TestFinalTaskBudgetDoesNotBypassReview(t *testing.T) {
	got := DecideAdvance(advanceFacts(AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 500}, IsLast: true}))
	if got.Next == "finish" || got.Status == StatusDone {
		t.Fatalf("final task over budget skipped its review: %+v", got)
	}
}

func TestDecideAdvanceCompletedReview(t *testing.T) {
	over := &Usage{Input: 100}
	for _, c := range []struct {
		name    string
		facts   AdvanceFacts
		wantOut string
	}{
		{"reviewed and over budget is done but stops", AdvanceFacts{CheckExit: exit(0), Usage: over, ReviewComplete: true}, "stop:budget"},
		{"reviewed final task finishes", AdvanceFacts{CheckExit: exit(0), Usage: over, ReviewComplete: true, IsLast: true}, "finish"},
		{"reviewed within budget moves on", AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 1}, ReviewComplete: true}, "next"},
	} {
		got := DecideAdvance(advanceFacts(c.facts))
		if got.Status != StatusDone || got.String() != c.wantOut {
			t.Errorf("%s: %+v, want done %s", c.name, got, c.wantOut)
		}
	}
	if got := DecideAdvance(advanceFacts(AdvanceFacts{CheckExit: exit(0), Usage: over, ReviewComplete: true, IsLast: true})); got.Reason != "budget" {
		t.Errorf("a final task over budget lost its diagnostic: %+v", got)
	}
}

func TestDecideAdvanceFailuresOutrankReview(t *testing.T) {
	if got := DecideAdvance(advanceFacts(AdvanceFacts{Stuck: true, CheckExit: exit(1)})); got.String() != "stop:stuck" {
		t.Errorf("stuck: %+v", got)
	}
	if got := DecideAdvance(advanceFacts(AdvanceFacts{CheckExit: exit(1), Usage: &Usage{Input: 500}})); got.Status != StatusRed || got.String() != "stop:red" {
		t.Errorf("red: %+v", got)
	}
}

// Once its review round is complete (or the triage gate skipped it), a task
// completes on the check and the budget alone.
func TestLegacyNoJudgeStillCompletes(t *testing.T) {
	for _, c := range []struct {
		name  string
		facts AdvanceFacts
		want  string
	}{
		{"within budget", AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 1}, ReviewComplete: true}, "next"},
		{"over budget stops with the task done", AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 500}, ReviewComplete: true}, "stop:budget"},
		{"unmeasured usage moves on", AdvanceFacts{CheckExit: exit(0), ReviewComplete: true}, "next"},
		{"no check command", AdvanceFacts{Usage: &Usage{Input: 1}, IsLast: true, ReviewComplete: true}, "finish"},
	} {
		got := DecideAdvance(advanceFacts(c.facts))
		if got.Status != StatusDone || got.String() != c.want {
			t.Errorf("%s: %+v, want done %s", c.name, got, c.want)
		}
	}
	if got := DecideAdvance(AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 1 << 40}, ReviewComplete: true}); got.String() != "next" {
		t.Errorf("budget 0 must be off: %+v", got)
	}
}

func TestDecideStaysACompatibilityWrapper(t *testing.T) {
	a := Decide(false, exit(0), &Usage{Input: 500}, 100, false)
	b := DecideAdvance(AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 500}, Budget: 100, ReviewComplete: true})
	if a != b {
		t.Fatalf("Decide %+v != DecideAdvance %+v", a, b)
	}
}

// The reviewer pane's verdict decides a reviewed task: clean is done, changes
// go back to the builder until the round cap, and then a person decides.
func TestDecideAdvanceReviewChanges(t *testing.T) {
	base := AdvanceFacts{CheckExit: exit(0), Usage: &Usage{Input: 1}}
	for _, c := range []struct {
		name       string
		verdict    string
		rounds     int
		wantStatus string
		wantOut    string
	}{
		{"changes in round 1 go back to the builder", "changes", 1, StatusSent, "fix"},
		{"changes in round 2 go back to the builder", "changes", 2, StatusSent, "fix"},
		{"changes at the cap stop for a person", "changes", MaxReviewRounds, StatusStuck, "stop:review_rounds"},
		{"clean is done", "clean", 1, StatusDone, "next"},
	} {
		f := base
		f.ReviewVerdict, f.ReviewRounds = c.verdict, c.rounds
		got := DecideAdvance(advanceFacts(f))
		if got.Status != c.wantStatus || got.String() != c.wantOut {
			t.Errorf("%s: %+v, want %s %s", c.name, got, c.wantStatus, c.wantOut)
		}
	}
	// A fix spends more on a task that is over its budget: stop, unaccepted.
	over := base
	over.Usage, over.ReviewVerdict, over.ReviewRounds = &Usage{Input: 100}, "changes", 1
	if got := DecideAdvance(advanceFacts(over)); got.Status != StatusStuck || got.String() != "stop:budget" {
		t.Errorf("changes over budget: %+v, want stuck stop:budget", got)
	}
	if MaxReviewRounds != 3 {
		t.Fatalf("MaxReviewRounds = %d, want 3", MaxReviewRounds)
	}
	f := base
	f.ReviewVerdict, f.ReviewRounds, f.IsLast = "clean", 1, true
	if got := DecideAdvance(advanceFacts(f)); got.String() != "finish" || got.Status != StatusDone {
		t.Errorf("a clean last task: %+v, want done finish", got)
	}
}

// A reviewer that edited the tree did not review what the check ran against,
// so its round counts for nothing, whatever it said.
func TestDecideAdvanceReviewThatEditedTheTreeIsInvalid(t *testing.T) {
	for _, verdict := range []string{"clean", "changes"} {
		f := AdvanceFacts{CheckExit: exit(0), ReviewVerdict: verdict, ReviewRounds: 1, ReviewEditedTree: true}
		if got := DecideAdvance(advanceFacts(f)); got.Status != StatusStuck || got.String() != "stop:review_edited_tree" {
			t.Errorf("%s: %+v, want stuck stop:review_edited_tree", verdict, got)
		}
	}
}
