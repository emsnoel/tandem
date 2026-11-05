package main

import (
	"os"
	"testing"
)

// A check that exited non-zero and printed nothing has an empty log. That is a
// fact about the check, not a log that could not be read.
func TestEmptyCheckLogSaysTheCheckPrintedNothing(t *testing.T) {
	d, open := openPaused(t, "check_failed")
	seedFailedCheck(t, open, d, "")
	if got, want := reasonOf(t, open), "The check exited 1 and printed nothing."; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestBlankCheckLogSaysTheCheckPrintedOnlyBlankLines(t *testing.T) {
	d, open := openPaused(t, "check_failed")
	seedFailedCheck(t, open, d, "\n\n  \n")
	if got, want := reasonOf(t, open), "The check exited 1 and printed only blank lines."; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

// A log that is really missing still gives no fact, and the note says so.
func TestMissingCheckLogStillGivesNoFact(t *testing.T) {
	d, open := openPaused(t, "check_failed")
	h := seedFailedCheck(t, open, d, "")
	if err := os.Remove(artifactFile(t, d, h)); err != nil {
		t.Fatal(err)
	}
	if got := reasonOf(t, open); got != "" {
		t.Fatalf("reason = %q, want none", got)
	}
}
