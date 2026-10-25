package plan

import (
	"strings"
	"testing"
)

const threeTasks = `# Live channel history

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan.

**Goal:** remember the channels a viewer watched.

## Global Constraints

- No new dependencies.

---

### Task 1: Storage

Add the table.

### Task 2: Selector

Write the selector.

` + "```md\n### Task 9: not a task\n```" + `

### Task 3: Screen

Show the rail.

## Final verification

Run the check once more.
`

func TestSplitFindsEachTask(t *testing.T) {
	p := Split(threeTasks)
	if p.Title != "Live channel history" {
		t.Fatalf("title = %q", p.Title)
	}
	if len(p.Tasks) != 3 {
		t.Fatalf("got %d tasks, want 3: %+v", len(p.Tasks), p.Tasks)
	}
	for i, want := range []string{"Storage", "Selector", "Screen"} {
		if p.Tasks[i].N != i+1 || p.Tasks[i].Title != want {
			t.Errorf("task %d = {%d %q}, want {%d %q}", i, p.Tasks[i].N, p.Tasks[i].Title, i+1, want)
		}
	}
}

func TestSplitIgnoresTaskHeadingsInsideCodeFences(t *testing.T) {
	p := Split(threeTasks)
	if !strings.Contains(p.Tasks[1].Body, "### Task 9: not a task") {
		t.Errorf("the fenced heading left task 2:\n%s", p.Tasks[1].Body)
	}
}

func TestSplitDropsTheSubSkillLineFromThePreamble(t *testing.T) {
	p := Split(threeTasks)
	if strings.Contains(p.Preamble, "REQUIRED SUB-SKILL") {
		t.Errorf("preamble still asks for a sub-skill:\n%s", p.Preamble)
	}
	if !strings.Contains(p.Preamble, "**Goal:** remember") || !strings.Contains(p.Preamble, "No new dependencies.") {
		t.Errorf("preamble lost the goal or the constraints:\n%s", p.Preamble)
	}
	if strings.Contains(p.Preamble, "# Live channel history") {
		t.Errorf("preamble repeats the title:\n%s", p.Preamble)
	}
}

func TestSplitGivesTheEpilogueToTheLastTask(t *testing.T) {
	p := Split(threeTasks)
	if !strings.Contains(p.Tasks[2].Body, "Run the check once more.") {
		t.Errorf("last task lost the epilogue:\n%s", p.Tasks[2].Body)
	}
	if strings.Contains(p.Tasks[1].Body, "Final verification") {
		t.Errorf("task 2 took the epilogue")
	}
}

func TestSplitGivesTextBetweenTasksToTheNextTask(t *testing.T) {
	text := "# P\n\n## Task 1: A\n\na\n\n# Phase two\n\nabout phase two\n\n## Task 2: B\n\nb\n"
	p := Split(text)
	if len(p.Tasks) != 2 {
		t.Fatalf("got %d tasks", len(p.Tasks))
	}
	if strings.Contains(p.Tasks[0].Body, "phase two") {
		t.Errorf("task 1 took the next section:\n%s", p.Tasks[0].Body)
	}
	if !strings.Contains(p.Tasks[1].Body, "about phase two") || !strings.Contains(p.Tasks[1].Body, "## Task 2: B") {
		t.Errorf("task 2 lost the section before it or its own heading:\n%s", p.Tasks[1].Body)
	}
}

func TestSplitKeepsTheTaskLevelFixed(t *testing.T) {
	// A deeper "#### Task" inside a ### task is a sub-heading, not a task.
	text := "# P\n\n### Task 1: A\n\n#### Task notes\n\nx\n\n### Task 2: B\n\ny\n"
	p := Split(text)
	if len(p.Tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(p.Tasks))
	}
}

func TestSplitWithoutTaskHeadingsIsOneTask(t *testing.T) {
	text := "# Fix the typo\n\nChange teh to the.\n"
	p := Split(text)
	if len(p.Tasks) != 1 || p.Tasks[0].N != 1 {
		t.Fatalf("got %+v", p.Tasks)
	}
	if p.Preamble != "" || !strings.Contains(p.Tasks[0].Body, "Change teh to the.") {
		t.Errorf("preamble %q, body %q", p.Preamble, p.Tasks[0].Body)
	}
	if p.Tasks[0].Title != "Fix the typo" {
		t.Errorf("title = %q", p.Tasks[0].Title)
	}
}

func TestBriefHoldsThePreambleAndOnlyItsTask(t *testing.T) {
	p := Split(threeTasks)
	b := p.Brief(1)
	for _, want := range []string{"Live channel history", "Task 2 of 3: Selector", "**Goal:** remember", "Write the selector."} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q:\n%s", want, b)
		}
	}
	for _, not := range []string{"Add the table.", "Show the rail."} {
		if strings.Contains(b, not) {
			t.Errorf("brief holds another task's text %q", not)
		}
	}
}
