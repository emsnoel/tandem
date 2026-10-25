package build

import (
	"fmt"
	"strings"
	"testing"
)

func finishedTwo() *Build {
	return &Build{
		ID: "b1", Title: "Greeting fixes", Plan: "/repo/p.md", Builder: "builder", Planner: "planner",
		Budget: 3000000, State: StateRunning, Current: 3,
		Tasks: []Task{
			{Title: "Punctuate", Status: StatusDone, Files: []string{"src/greet.sh"}, CheckExit: exit(0),
				CheckLog: "/repo/.tandem-relay/build/checks/task-1.log", Report: "/r/task-1.report.md",
				Usage: &Usage{Calls: 12, Input: 400000, First: 30000, Peak: 41000}},
			{Title: "Farewell", Status: StatusDone, Files: []string{"src/farewell.sh"}, CheckExit: exit(0)},
			{Title: "Review", Status: StatusPending},
		},
	}
}

func TestLedgerIsEmptyBeforeAnyTaskIsDone(t *testing.T) {
	b := finishedTwo()
	b.Tasks[0].Status, b.Tasks[1].Status = StatusSent, StatusPending
	if got := Ledger(b, nil, nil); got != "" {
		t.Errorf("Ledger = %q", got)
	}
}

func TestStatusSummarySaysAReviewingTaskIsInReview(t *testing.T) {
	b := finishedTwo()
	b.Current = 3
	b.Tasks[2].Status = StatusReviewing
	if got := StatusSummary(b); got != "task 3 of 3 is in review" {
		t.Fatalf("StatusSummary = %q", got)
	}
}

func TestLedgerListsDoneTasksWithTheirFactsAndNotes(t *testing.T) {
	got := Ledger(finishedTwo(), map[int]string{1: "Added punctuation.\nThe greet helper is now greetLine()."}, nil)
	for _, want := range []string{
		"Task 1 of 3: Punctuate", "done", "src/greet.sh", "check exit 0",
		"The greet helper is now greetLine().", "Task 2 of 3: Farewell", "src/farewell.sh",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ledger lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Task 3 of 3") {
		t.Errorf("ledger lists a task not done:\n%s", got)
	}
}

func TestLedgerCapsANoteAndAFileList(t *testing.T) {
	b := finishedTwo()
	for i := 0; i < 40; i++ {
		b.Tasks[0].Files = append(b.Tasks[0].Files, fmt.Sprintf("f%d", i))
	}
	got := Ledger(b, map[int]string{1: strings.Repeat("line\n", 60)}, nil)
	if n := strings.Count(got, "line\n"); n > noteLines {
		t.Errorf("note has %d lines, cap %d", n, noteLines)
	}
	if !strings.Contains(got, "and 21 more") {
		t.Errorf("file list not capped:\n%s", got)
	}
}

func TestReportHoldsTheLedgerTableAndEachNote(t *testing.T) {
	b := finishedTwo()
	b.State, b.Current = StateFinished, 3
	b.Tasks[2].Status = StatusDone
	got := Report(b, map[int]string{1: "note one", 3: "note three"}, nil)
	for _, want := range []string{"Greeting fixes", "/repo/p.md", "finished", "| 1 | Punctuate | done | 0 | 1 | 12 | 400000 | 41000 |", "note one", "note three"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestPlannerMessageNamesTheRemedyForEachEnding(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(b *Build)
		want  []string
	}{
		{"finished", func(b *Build) { b.State = StateFinished }, []string{
			"All 3 tasks", "tandem-ask builder", "tandem-unpair --verified R1"}},
		{"red", func(b *Build) {
			b.State, b.StopReason, b.StopTask, b.Current = StateStopped, "red", 2, 2
			b.Tasks[1].Status, b.Tasks[1].CheckExit = StatusRed, exit(1)
			b.Tasks[1].CheckLog = "/log/task-2.log"
		}, []string{"stopped at task 2 of 3", "check", "exit 1", "/log/task-2.log",
			"hand back task 2 again", "reruns the same check",
			`tandem build accept --task 2 --because "why"`, "tandem-handoff", "tandem-unpair --verified R1"}},
		{"stuck", func(b *Build) {
			b.State, b.StopReason, b.StopTask, b.Current = StateStopped, "stuck", 2, 2
			b.Tasks[1].Status, b.Tasks[1].CheckExit = StatusStuck, exit(0)
		}, []string{"could not finish", "Tandem's check after task 2 passed",
			"hand back task 2 again", `tandem build accept --task 2 --because "why"`}},
		{"stuck, check failed", func(b *Build) {
			b.State, b.StopReason, b.StopTask, b.Current = StateStopped, "stuck", 2, 2
			b.Tasks[1].Status, b.Tasks[1].CheckExit = StatusStuck, exit(1)
		}, []string{"could not finish", "Tandem's check after task 2 failed with exit 1"}},
		{"budget", func(b *Build) {
			b.State, b.StopReason, b.StopTask, b.Current = StateStopped, "budget", 1, 2
			b.Tasks[0].Usage.Input = 4100000
		}, []string{"4100000 input tokens", "3000000", "tandem build status", "carry on from task 2"}},
		{"handoff", func(b *Build) {
			b.State, b.StopReason, b.StopTask, b.Current = StateStopped, "handoff", 2, 3
			b.StopDetail = "the review gate refused (rc 24)"
		}, []string{"could not send task 3", "the review gate refused (rc 24)", "carry on from task 3"}},
	} {
		b := finishedTwo()
		c.setup(b)
		got := PlannerMessage(b, "R1", "/r/R1.md")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: message lacks %q:\n%s", c.name, w, got)
			}
		}
		if !strings.Contains(got, "/r/R1.md") {
			t.Errorf("%s: message lacks the report path", c.name)
		}
	}
}

func TestAnAcceptedTaskShowsItsReasonInTheLedgerAndReport(t *testing.T) {
	b := finishedTwo()
	b.Tasks[0].AcceptedFrom, b.Tasks[0].Accepted = StatusStuck, "check passed; sandbox killed the run"
	want := "Accepted by the planner after a stuck stop: check passed; sandbox killed the run"
	if got := Ledger(b, nil, nil); !strings.Contains(got, want) {
		t.Errorf("ledger lacks the acceptance:\n%s", got)
	}
	if got := Report(b, nil, nil); !strings.Contains(got, want) {
		t.Errorf("report lacks the acceptance:\n%s", got)
	}
}
