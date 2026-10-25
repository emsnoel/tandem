package gate

import (
	"testing"

	"github.com/emsnoel/tandem/internal/mailbox"
)

func TestPreRefusesWorkingBuilderWithDetail(t *testing.T) {
	v := Pre(Input{BuilderPane: "p2", Status: "working"})
	if v.Code != 9 || len(v.Detail) != 1 || v.Detail[0] != "working" {
		t.Fatalf("want 9 with detail [working], got %d %v", v.Code, v.Detail)
	}
}

func TestPreRefusesBlockedBuilderWithItsOwnDetail(t *testing.T) {
	v := Pre(Input{BuilderPane: "p2", Status: "blocked"})
	if v.Code != 9 || len(v.Detail) != 1 || v.Detail[0] != "blocked" {
		t.Fatalf("want 9 with detail [blocked], got %d %v", v.Code, v.Detail)
	}
}

func TestPreRefusesWhenNeverHandedBack(t *testing.T) {
	if v := Pre(Input{BuilderPane: "p2", Status: "idle"}); v.Code != 4 {
		t.Fatalf("want 4 when there are no reports, got %d", v.Code)
	}
}

func TestPreRefusesUnsignedHandbacksAndNamesThem(t *testing.T) {
	v := Pre(Input{BuilderPane: "p2", Status: "idle", ReportsPending: []string{"id-1", "id-2"}})
	if v.Code != 5 || len(v.Detail) != 2 {
		t.Fatalf("want 5 with both pending ids, got %d %v", v.Code, v.Detail)
	}
}

func TestPreRefusesWithNoCheckCommand(t *testing.T) {
	in := Input{BuilderPane: "p2", Status: "idle", ReportsOK: []string{"id-1"}}
	if v := Pre(in); v.Code != 6 {
		t.Fatalf("want 6 without a check command, got %d", v.Code)
	}
}

func TestPreAllowsWhenSignedOffAndCheckAvailable(t *testing.T) {
	in := Input{BuilderPane: "p2", Status: "idle", ReportsOK: []string{"id-1"}, HasCheckCommand: true}
	if v := Pre(in); v.Code != 0 {
		t.Fatalf("want allow, got %d", v.Code)
	}
}

func TestForceBypassesEveryGate(t *testing.T) {
	if v := Pre(Input{BuilderPane: "p2", Status: "working", Force: true}); v.Code != 0 {
		t.Fatalf("want allow under --force, got %d", v.Code)
	}
}

// An absent builder pane means there is nothing left to close, so every gate
// is skipped. A builder name cannot be used as this presence signal.
func TestPreAllowsWhenNoBuilderPaneRemains(t *testing.T) {
	in := Input{BuilderPane: "", Status: "idle"}
	if v := Pre(in); v.Code != 0 {
		t.Fatalf("want allow when no builder pane remains, got %d", v.Code)
	}
}

func TestPostAllowsWhenNoBuilderPaneRemains(t *testing.T) {
	in := Input{BuilderPane: "", Status: "idle", StatusNow: "working"}
	if v := Post(in); v.Code != 0 {
		t.Fatalf("want allow when no builder pane remains, got %d", v.Code)
	}
}

func TestPostRefusesWhenBuilderMovedDuringTheCheck(t *testing.T) {
	in := Input{BuilderPane: "p2", Status: "idle", StatusNow: "working", HasCheckCommand: true}
	v := Post(in)
	if v.Code != 8 || len(v.Detail) == 0 || v.Detail[0] != "status" {
		t.Fatalf("want 8 with status detail, got %d %v", v.Code, v.Detail)
	}
}

func TestPostRefusesStaleGreenEvidence(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		kind string
	}{
		{"report changed", Input{BuilderPane: "p2", ReportsBefore: mailbox.Snapshot{"id": 1}, ReportsAfter: mailbox.Snapshot{"id": 2}}, "reports"},
		{"tree changed", Input{BuilderPane: "p2", TreeMeasured: true, TreeBefore: " M file", TreeAfter: " M other"}, "tree"},
		// A CLEAN tree going dirty during the check is the case bin/tandem-unpair
		// could not see: its empty fingerprint meant both "clean" and "unreadable".
		{"clean tree went dirty", Input{BuilderPane: "p2", TreeMeasured: true, TreeBefore: "", TreeAfter: " M file"}, "tree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Post(tt.in)
			if v.Code != 8 || len(v.Detail) == 0 || v.Detail[0] != tt.kind {
				t.Fatalf("want 8 with %s detail, got %d %v", tt.kind, v.Code, v.Detail)
			}
		})
	}
}

// When git could not be read, the comparison is skipped, not treated as a
// change.
func TestPostSkipsTreeCheckWhenUnmeasured(t *testing.T) {
	in := Input{BuilderPane: "p2", TreeMeasured: false, TreeBefore: "", TreeAfter: " M file"}
	if v := Post(in); v.Code != 0 {
		t.Fatalf("want allow when the tree could not be measured, got %d %v", v.Code, v.Detail)
	}
}

// A durable run is signed off by its own evidence, never by the reports
// directory, which only legacy builds write.
func durableRun(unmet ...[]string) *DurableRun {
	d := &DurableRun{RunID: "r1", State: "finished"}
	for i, u := range unmet {
		d.Tasks = append(d.Tasks, DurableTask{ID: "task-" + string(rune('1'+i)), Unmet: u})
	}
	return d
}

func TestPreSignsOffAFinishedDurableRunOnItsEvidenceAlone(t *testing.T) {
	in := Input{BuilderPane: "p2", Status: "idle", HasCheckCommand: true, Durable: durableRun(nil, nil),
		ReportsPending: []string{"old-legacy-report"}}
	if v := Pre(in); v.Code != 0 {
		t.Fatalf("a finished run with full evidence, beside an old legacy report: got %d %v", v.Code, v.Detail)
	}
}

func TestPreRefusesADurableRunWithTasksLackingEvidenceAndNamesThem(t *testing.T) {
	in := Input{BuilderPane: "p2", Status: "idle", HasCheckCommand: true, Durable: durableRun(nil, []string{"check", "review"}, []string{"acceptance"})}
	v := Pre(in)
	if v.Code != 5 || len(v.Detail) != 2 || v.Detail[0] != "task-2 check review" || v.Detail[1] != "task-3 acceptance" {
		t.Fatalf("want 5 naming task-2 and task-3, got %d %v", v.Code, v.Detail)
	}
}

func TestPreRefusesADurableRunWithNothingAccepted(t *testing.T) {
	in := Input{BuilderPane: "p2", Status: "idle", HasCheckCommand: true, Durable: durableRun([]string{"acceptance"}, []string{"acceptance"})}
	if v := Pre(in); v.Code != 4 {
		t.Fatalf("want 4 when no task was accepted, got %d %v", v.Code, v.Detail)
	}
}

func TestPreKeepsTheOtherGatesForADurableRun(t *testing.T) {
	if v := Pre(Input{BuilderPane: "p2", Status: "working", HasCheckCommand: true, Durable: durableRun(nil)}); v.Code != 9 {
		t.Fatalf("a working builder: got %d", v.Code)
	}
	if v := Pre(Input{BuilderPane: "p2", Status: "idle", Durable: durableRun(nil)}); v.Code != 6 {
		t.Fatalf("no check command: got %d", v.Code)
	}
	if v := Pre(Input{BuilderPane: "p2", Status: "idle", Force: true, Durable: durableRun([]string{"acceptance"})}); v.Code != 0 {
		t.Fatalf("--force: got %d", v.Code)
	}
}
