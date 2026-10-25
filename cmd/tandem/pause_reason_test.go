package main

import (
	"strings"
	"testing"
)

func TestReviewUnknownPauseSaysHowToRecordTheReview(t *testing.T) {
	why := fixedPauseReason("review_unknown")
	for _, want := range []string{"review-record", "--waive review"} {
		if !strings.Contains(why, want) {
			t.Errorf("the review_unknown reason %q does not mention %q", why, want)
		}
	}
}

func TestFixedPauseReasonIsEmptyForAPauseThatNeedsTheRunsRecord(t *testing.T) {
	if why := fixedPauseReason("dispatch_failed"); why != "" {
		t.Fatalf("dispatch_failed is explained from the run's effects, got %q", why)
	}
}
