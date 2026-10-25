package state

import (
	"errors"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func TestPutUsageIsUniquePerSourceAndValidated(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	job := sd.job(t, "builder", 31)
	rec := UsageRecord{ID: hexID(9001), AttemptID: sd.AttemptID, JobID: job, SourceID: strings.Repeat("a", 32), Model: "m",
		Usage: workflow.Usage{Input: workflow.Int64(100), CachedInput: workflow.Int64(40), Output: workflow.Int64(5), DurationMS: 7, Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}}
	if err := s.PutUsage(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// The same source again, identical: one row. Different figures: a conflict.
	if err := s.PutUsage(ctx, rec); err != nil {
		t.Fatalf("recording the same source again: %v", err)
	}
	if n := sd.count(t, "usage_records"); n != 1 {
		t.Errorf("%d rows", n)
	}
	changed := rec
	changed.Usage.Input = workflow.Int64(101)
	if err := s.PutUsage(ctx, changed); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("one event with two sets of figures: %v", err)
	}
	// Different id, same source: still the same event.
	again := rec
	again.ID = hexID(9002)
	if err := s.PutUsage(ctx, again); err != nil || sd.count(t, "usage_records") != 1 {
		t.Errorf("%v, %d rows", err, sd.count(t, "usage_records"))
	}

	for name, mut := range map[string]func(*UsageRecord){
		"cached over input": func(r *UsageRecord) { r.Usage.CachedInput = workflow.Int64(101) },
		"negative":          func(r *UsageRecord) { r.Usage.Output = workflow.Int64(-1) },
		"bad completeness":  func(r *UsageRecord) { r.Usage.Completeness = "mostly" },
		"unknown attempt":   func(r *UsageRecord) { r.AttemptID = hexID(404) },
		"another job":       func(r *UsageRecord) { r.JobID = hexID(405) },
		"short source":      func(r *UsageRecord) { r.SourceID = "abc" },
		"no model":          func(r *UsageRecord) { r.Model = "" },
	} {
		r := rec
		r.ID, r.SourceID = hexID(9100+len(name)), strings.Repeat("b", 31)+string(rune('a'+len(name)%20))
		mut(&r)
		if err := s.PutUsage(ctx, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n := sd.count(t, "usage_records"); n != 1 {
		t.Errorf("%d rows after the refusals", n)
	}
}

func TestUsageMarkersAreReplacedWhenTheGapCloses(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	job := sd.job(t, "builder", 32)
	marker := UsageRecord{ID: hexID(9201), AttemptID: sd.AttemptID, JobID: job, SourceID: strings.Repeat("c", 32), Model: "unknown",
		Usage: workflow.Usage{Completeness: workflow.UsageUnavailable, InputSemantics: workflow.SemanticsUnknown}}
	if err := s.PutUsage(ctx, marker); err != nil {
		t.Fatal(err)
	}
	real := UsageRecord{ID: hexID(9202), AttemptID: sd.AttemptID, JobID: job, SourceID: strings.Repeat("d", 32), Model: "m",
		Usage: workflow.Usage{Input: workflow.Int64(10), CachedInput: workflow.Int64(0), Output: workflow.Int64(1), Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}}
	// The log became readable: the marker for it is cleared in the same step.
	if err := s.PutUsage(ctx, real, marker.SourceID); err != nil {
		t.Fatal(err)
	}
	if n := sd.count(t, "usage_records"); n != 1 {
		t.Errorf("%d rows: the stale gap marker must go with the record that closed it", n)
	}
	// Clearing something that is not a marker, or not there, does nothing.
	if err := s.PutUsage(ctx, real, strings.Repeat("e", 32), real.SourceID); err != nil {
		t.Fatal(err)
	}
	if n := sd.count(t, "usage_records"); n != 1 {
		t.Errorf("a real record was cleared as if it were a marker: %d", n)
	}
}

func TestListUsageCarriesWhatAttributionNeeds(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	job := sd.job(t, "builder", 33)
	rec := UsageRecord{ID: hexID(9301), AttemptID: sd.AttemptID, JobID: job, SourceID: strings.Repeat("f", 32), Model: "gpt-x",
		Usage: workflow.Usage{Input: workflow.Int64(10), CachedInput: workflow.Int64(2), Output: workflow.Int64(1), Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}}
	if err := s.PutUsage(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListUsage(ctx, sd.RunID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	r := rows[0]
	if r.JobKind != "builder" || r.AttemptOrdinal != 1 || r.ParentAttemptID != "" || r.TaskID != sd.TaskID || r.Model != "gpt-x" || *r.Usage.Input != 10 || r.JobConfigHash == "" {
		t.Errorf("%+v", r)
	}
	if rows, _ := s.ListUsage(ctx, hexID(808)); len(rows) != 0 {
		t.Errorf("another run's usage: %v", rows)
	}
}
