package build

import (
	"fmt"
	"strings"
)

const (
	// noteLines caps each task's report in the ledger. The prompt asks the
	// builder for at most 20 lines; a longer one is cut rather than carried
	// into every later task's context.
	noteLines = 20
	fileCap   = 20
)

func checkText(t Task) string {
	if t.CheckExit == nil {
		return "no check command"
	}
	return fmt.Sprintf("check exit %d", *t.CheckExit)
}

// acceptedText is the line a planner-accepted task carries, or "".
func acceptedText(t Task) string {
	if t.Accepted == "" {
		return ""
	}
	return fmt.Sprintf("Accepted by the planner after a %s stop: %s", t.AcceptedFrom, t.Accepted)
}

func files(fs []string) string {
	if len(fs) == 0 {
		return "none"
	}
	if len(fs) <= fileCap {
		return strings.Join(fs, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(fs[:fileCap], ", "), len(fs)-fileCap)
}

func capLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "(cut at "+fmt.Sprint(n)+" lines)")
	}
	return strings.Join(lines, "\n")
}

func indent(s, pad string) string {
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// Ledger is what the builder is told about earlier tasks: for each one done,
// what tandem measured and the builder's own note. reports maps a task's
// position to its first-pass note; reviews carries the separate review note.
// Both notes are capped separately. "" before any task is done.
func Ledger(b *Build, reports, reviews map[int]string) string {
	var s strings.Builder
	for i, t := range b.Tasks {
		if t.Status != StatusDone {
			continue
		}
		fmt.Fprintf(&s, "Task %d of %d: %s. %s, %s.\n", i+1, len(b.Tasks), t.Title, t.Status, checkText(t))
		fmt.Fprintf(&s, "  Files changed: %s\n", files(t.Files))
		if a := acceptedText(t); a != "" {
			fmt.Fprintf(&s, "  %s\n", a)
		}
		if note := strings.TrimSpace(reports[i+1]); note != "" {
			s.WriteString("  Its builder's note:\n")
			s.WriteString(indent(capLines(note, noteLines), "    "))
			s.WriteString("\n")
		}
		if note := strings.TrimSpace(reviews[i+1]); note != "" {
			s.WriteString("  Review round:\n")
			s.WriteString(indent(capLines(note, noteLines), "    "))
			s.WriteString("\n")
		}
	}
	return s.String()
}

func dash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// StatusSummary describes the task the build loop is currently waiting on.
func StatusSummary(b *Build) string {
	if task := b.Task(b.Current); task != nil && task.Status == StatusReviewing {
		return fmt.Sprintf("task %d of %d is in review", b.Current, len(b.Tasks))
	}
	return fmt.Sprintf("task %d of %d is next", b.Current, len(b.Tasks))
}

// Report is the handback report the planner verifies: the ledger as a table,
// then each task's facts and its full builder and review reports.
func Report(b *Build, reports, reviews map[int]string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "# Build report: %s\n\n", b.Title)
	fmt.Fprintf(&s, "Plan: %s\n", b.Plan)
	state := b.State
	if b.State == StateStopped {
		state = fmt.Sprintf("stopped at task %d of %d (%s)", b.StopTask, len(b.Tasks), b.StopReason)
		if b.StopDetail != "" {
			state += ": " + b.StopDetail
		}
	}
	fmt.Fprintf(&s, "State: %s\n\n", state)
	s.WriteString("Written by tandem. Status, files, check and tokens are measured; each task's notes are the builder's own claims.\n\n")
	s.WriteString(Table(b))
	for i, t := range b.Tasks {
		if t.Status == StatusPending {
			continue
		}
		fmt.Fprintf(&s, "\n## Task %d of %d: %s\n\n", i+1, len(b.Tasks), t.Title)
		fmt.Fprintf(&s, "Status: %s. %s.", t.Status, checkText(t))
		if t.CheckLog != "" {
			fmt.Fprintf(&s, " Log: %s", t.CheckLog)
		}
		fmt.Fprintf(&s, "\n\nFiles changed: %s\n", strings.Join(t.Files, ", "))
		if a := acceptedText(t); a != "" {
			fmt.Fprintf(&s, "\n%s\n", a)
		}
		if note := strings.TrimSpace(reports[i+1]); note != "" {
			s.WriteString("\nThe builder's report:\n\n")
			s.WriteString(note)
			s.WriteString("\n")
		}
		if note := strings.TrimSpace(reviews[i+1]); note != "" {
			s.WriteString("\nReview round:\n\n")
			s.WriteString(note)
			s.WriteString("\n")
		}
	}
	return s.String()
}

// PlannerMessage is what the planner is sent when the loop finishes or stops.
func PlannerMessage(b *Build, reportID, reportPath string) string {
	n := len(b.Tasks)
	var s strings.Builder
	if b.State == StateFinished {
		fmt.Fprintf(&s, "[build from %s] All %d tasks of the plan are done, each in a fresh conversation.\n", b.Builder, n)
	} else {
		fmt.Fprintf(&s, "[build from %s] The build stopped at task %d of %d.\n", b.Builder, b.StopTask, n)
	}
	fmt.Fprintf(&s, "\nPlan:   %s\nReport: %s\n\n", b.Plan, reportPath)

	if b.State != StateFinished {
		stopped := b.Task(b.StopTask)
		switch b.StopReason {
		case "red":
			exit, log := "?", ""
			if stopped != nil && stopped.CheckExit != nil {
				exit = fmt.Sprint(*stopped.CheckExit)
			}
			if stopped != nil {
				log = stopped.CheckLog
			}
			fmt.Fprintf(&s, "Tandem ran the check after task %d and it failed with exit %s.\nLog: %s\n\n", b.StopTask, exit, log)
		case "stuck":
			fmt.Fprintf(&s, "%s said it could not finish task %d. Its report says why.\n", b.Builder, b.StopTask)
			switch {
			case stopped != nil && stopped.CheckExit != nil && *stopped.CheckExit == 0:
				fmt.Fprintf(&s, "Tandem's check after task %d passed. That does not show the task was finished;\n"+
					"read the report before you accept it.\n\n", b.StopTask)
			case stopped != nil && stopped.CheckExit != nil:
				fmt.Fprintf(&s, "Tandem's check after task %d failed with exit %d.\n\n", b.StopTask, *stopped.CheckExit)
			default:
				s.WriteString("\n")
			}
		case "budget":
			var input int64
			if stopped != nil && stopped.Usage != nil {
				input = stopped.Usage.Input
			}
			if fixHeld(stopped) {
				fmt.Fprintf(&s, "Task %d used %d input tokens, over the task budget of %d. %s asked for changes,\n"+
					"and the fix was not sent, so the task is not accepted. Its findings: %s\n\n",
					b.StopTask, input, b.Budget, b.Reviewer, stopped.ReviewReport)
				break
			}
			if stopped != nil && stopped.Review != nil && stopped.Review.Pending {
				fmt.Fprintf(&s, "Task %d used %d input tokens, over the task budget of %d, so its review has\n"+
					"not run and the task is not accepted. Raise the budget, then run `tandem-handoff`\n"+
					"to send the review to %s. `tandem build status` shows each\n"+
					"task's calls, input and peak context.\n\n", b.StopTask, input, b.Budget, b.Reviewer)
				break
			}
			fmt.Fprintf(&s, "Task %d used %d input tokens, over the task budget of %d. The task itself is\n"+
				"done and its check passed. Find out why before the next task spends as much:\n"+
				"`tandem build status` shows each task's calls, input and peak context.\n\n", b.StopTask, input, b.Budget)
		case "review_edited_tree":
			fmt.Fprintf(&s, "Task %d's reviewer changed the tree while it reviewed, so its review is not\n"+
				"counted and the task is not accepted. A reviewer reads; it never edits.\n\n", b.StopTask)
		case "review_rounds":
			report := ""
			if stopped != nil {
				report = stopped.ReviewReport
			}
			fmt.Fprintf(&s, "Task %d went through %d review rounds and %s still asked for changes, so\n"+
				"the task is not accepted. The last findings: %s\n\n", b.StopTask, MaxReviewRounds, b.Reviewer, report)
		case "handoff":
			fmt.Fprintf(&s, "Tandem could not send task %d: %s\n\n", b.Current, b.StopDetail)
		default:
			fmt.Fprintf(&s, "%s.\n\n", b.StopDetail)
		}
		switch b.StopReason {
		case "red", "stuck":
			fmt.Fprintf(&s, "%s's conversation still holds task %d. To have it fix the task there:\n\n"+
				"    tandem-ask %s \"what to fix, then hand back task %d again\"\n\n"+
				"The build carries on by itself when it does. Handing back with nothing changed\n"+
				"reruns the same check and stops again.\n\n"+
				"If the task is right as it stands, accept it on the record, then carry on:\n\n"+
				"    tandem build accept --task %d --because \"why\"\n\n"+
				"Or fix the plan and run `tandem-handoff`, which sends task %d again in a fresh\n"+
				"conversation.\n\n",
				b.Builder, b.StopTask, b.Builder, b.StopTask, b.StopTask, b.Current)
		case "budget":
			if !fixHeld(b.Task(b.StopTask)) {
				fmt.Fprintf(&s, "Run `tandem-handoff` to carry on from task %d.\n\n", b.Current)
				break
			}
			fmt.Fprintf(&s, "Find out why first: `tandem build status` shows each task's calls, input and peak\n"+
				"context. To have %s fix the findings in its conversation anyway:\n\n"+
				"    tandem-ask %s \"fix the findings, then hand back task %d again\"\n\n"+
				"Its next handback is reviewed again. If the task is right as it stands, accept it\n"+
				"on the record:\n\n"+
				"    tandem build accept --task %d --because \"why the findings do not apply\"\n\n",
				b.Builder, b.Builder, b.StopTask, b.StopTask)
		case "review_edited_tree":
			fmt.Fprintf(&s, "Read `git diff` to see what changed. If the task is right as it stands, accept\n"+
				"it on the record:\n\n"+
				"    tandem build accept --task %d --because \"why\"\n\n"+
				"Or undo the reviewer's edits and run `tandem-handoff` to carry on.\n\n", b.StopTask)
		case "review_rounds":
			fmt.Fprintf(&s, "Read the findings and the diff. To have %s fix them in its conversation:\n\n"+
				"    tandem-ask %s \"what to fix, then hand back task %d again\"\n\n"+
				"If the task is right as it stands, accept it on the record:\n\n"+
				"    tandem build accept --task %d --because \"why the findings do not apply\"\n\n",
				b.Builder, b.Builder, b.StopTask, b.StopTask)
		default:
			fmt.Fprintf(&s, "Run `tandem-handoff` to carry on from task %d.\n\n", b.Current)
		}
		fmt.Fprintf(&s, "To end the session here instead, verify the work so far and run:\n\n    tandem-unpair --verified %s\n", reportID)
		return s.String()
	}

	fmt.Fprintf(&s, "Read the report. The table and the files are what tandem measured; each task's\n"+
		"report is %s's own account, so treat it as a claim and not as evidence. Check it\n"+
		"against the tree before you accept it:\n\n"+
		"  - Run the project's check command yourself.\n"+
		"  - Read the diff, looking as hard for what the plan did not ask for as for\n"+
		"    what it did.\n"+
		"  - Take anything a report lists as unverified or unfinished seriously.\n\n"+
		"If anything is wrong, send it back:\n\n"+
		"    tandem-ask %s \"what needs fixing\"\n\n"+
		"%s's conversation holds only the last task. The earlier tasks' context is in\n"+
		"the report and the tree.\n\n"+
		"When you are genuinely satisfied, not merely finished reading, close the session:\n\n"+
		"    tandem-unpair --verified %s\n\n"+
		"That closes %s's pane. It is the last step and it is not reversible.\n",
		b.Builder, b.Builder, b.Builder, reportID, b.Builder)
	return s.String()
}

// fixHeld reports whether t stopped with a reviewer's findings unsent because
// its budget was spent: stuck, with a round that asked for changes.
func fixHeld(t *Task) bool {
	return t != nil && t.Status == StatusStuck && t.Review != nil && t.Review.Result == "changes"
}

// Table is the ledger as a markdown table: one row per task with what tandem
// measured. `tandem build status` prints it, and so does the report.
func Table(b *Build) string {
	var s strings.Builder
	s.WriteString("| Task | Title | Status | Check | Files | Calls | Input | Peak |\n")
	s.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i, t := range b.Tasks {
		check, calls, input, peak := "", "", "", ""
		if t.CheckExit != nil {
			check = fmt.Sprint(*t.CheckExit)
		}
		if t.Usage != nil {
			calls, input, peak = fmt.Sprint(t.Usage.Calls), fmt.Sprint(t.Usage.Input), fmt.Sprint(t.Usage.Peak)
		}
		fmt.Fprintf(&s, "| %d | %s | %s | %s | %d | %s | %s | %s |\n",
			i+1, t.Title, t.Status, dash(check), len(t.Files), dash(calls), dash(input), dash(peak))
	}
	return s.String()
}
