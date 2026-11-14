package controller

import (
	"strings"
	"testing"
)

const (
	waitingRule  = "run it in ONE blocking call with the longest timeout your tool accepts. Do not poll"
	checkIsOurs  = "Tandem runs the project's check itself after you hand back"
	leaveTheTest = "do not run the full check yourself unless the task needs its output"
)

// A builder that polls a 20-second check 21 times spends 3.0M input tokens
// (F19), so the footer says how to wait whether or not the run has a check.
func TestTheTaskFooterSaysHowToWait(t *testing.T) {
	for _, check := range []string{"", "make check"} {
		footer := promptFooter(promptData{Task: 1, CheckCommand: check})
		if !strings.Contains(footer, waitingRule) {
			t.Errorf("with check %q the footer does not say how to wait:\n%s", check, footer)
		}
		if strings.Index(footer, waitingRule) > strings.Index(footer, "When you finish") {
			t.Errorf("with check %q the waiting rule comes after the handback steps:\n%s", check, footer)
		}
	}
}

func TestTheTaskFooterLeavesTheCheckToTandem(t *testing.T) {
	footer := promptFooter(promptData{Task: 1, CheckCommand: "make check"})
	for _, want := range []string{checkIsOurs, leaveTheTest} {
		if !strings.Contains(footer, want) {
			t.Errorf("the footer is missing %q:\n%s", want, footer)
		}
	}
	// A run with no check has nothing for tandem to run, so the builder is not told to leave it.
	footer = promptFooter(promptData{Task: 1})
	for _, absent := range []string{checkIsOurs, leaveTheTest} {
		if strings.Contains(footer, absent) {
			t.Errorf("a run with no check has %q:\n%s", absent, footer)
		}
	}
}

// The footer offered --stuck without saying when to use it. These are the five
// findings the plan review looks for; a builder that meets one stops instead of
// improvising.
func TestTheTaskFooterSaysWhenToStop(t *testing.T) {
	footer := promptFooter(promptData{Task: 1})
	order := []string{
		"Do the task.",
		"Stop and hand back with --stuck",
		"needs something a later task introduces",
		"contradicts another step",
		"cannot run as written",
		"needs authority you do not have",
		"would not give the result the plan claims",
		"Stopping then is the correct outcome, not a failure",
		"When you finish",
	}
	at := -1
	for _, want := range order {
		i := strings.Index(footer, want)
		if i < 0 {
			t.Fatalf("the footer is missing %q:\n%s", want, footer)
		}
		if i < at {
			t.Errorf("%q is out of order:\n%s", want, footer)
		}
		at = i
	}
}
