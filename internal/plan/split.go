// Package plan splits an implementation plan into one brief per task, so a
// builder gets one task in a fresh conversation instead of the whole plan. A
// brief is the plan's shared opening (goal, architecture, constraints) plus one
// task. The split is mechanical and uses no model.
//
// Pure: it reads the text it is given.
package plan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Task is one "Task N" section of a plan. Body starts with its own heading.
type Task struct {
	N     int
	Title string
	Body  string
	// head is the index of the task's own heading among the lines of Body, or
	// -1 when the plan has no task headings. Text between two tasks goes to the
	// one after it, so the heading is not always the first line.
	head int
}

// Plan is a plan split at its task headings.
type Plan struct {
	Title    string
	Preamble string
	Tasks    []Task
}

var taskRE = regexp.MustCompile(`^(#{2,4})\s+Task\s+(\d+)\b[:.]?\s*(.*)$`)

// headingLevel returns the number of leading '#' of a markdown heading, or 0.
func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

func isFence(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// subSkill marks the header line superpowers plans carry, telling the reader
// to load a plan-execution workflow. Tandem runs the plan itself, one task per
// conversation, and loading those skills cost ~60k characters per build.
const subSkill = "REQUIRED SUB-SKILL"

// Split finds the plan's task headings: "Task N" headings at one level,
// ## to ####, fixed by the first one found, outside code fences. A task runs
// to the next task heading or to a shallower heading. Text between two tasks
// goes to the one after it, and text after the last task to the last task, so
// nothing the plan says is dropped. A plan with no task headings is one task.
func Split(text string) Plan {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var p Plan
	level := 0
	inFence := false
	var pre, pending, cur []string
	inTask := false
	headAt := 0
	flush := func() {
		if len(p.Tasks) > 0 {
			raw := strings.Join(cur, "\n")
			t := &p.Tasks[len(p.Tasks)-1]
			// The heading is never blank, so what TrimSpace cuts from the front
			// came from the text before it.
			lead := len(raw) - len(strings.TrimLeftFunc(raw, unicode.IsSpace))
			t.head = headAt - strings.Count(raw[:lead], "\n")
			t.Body = strings.TrimSpace(raw)
		}
	}
	for _, line := range lines {
		if isFence(line) {
			inFence = !inFence
		}
		h := 0
		var m []string
		if !inFence && !isFence(line) {
			h = headingLevel(line)
			m = taskRE.FindStringSubmatch(line)
		}
		if h == 1 && p.Title == "" {
			p.Title = strings.TrimSpace(line[1:])
			if len(p.Tasks) == 0 {
				continue
			}
		}
		if m != nil && (level == 0 || len(m[1]) == level) {
			if level == 0 {
				level = len(m[1])
			}
			flush()
			n, _ := strconv.Atoi(m[2])
			p.Tasks = append(p.Tasks, Task{N: n, Title: strings.TrimSpace(m[3])})
			headAt = len(pending)
			cur = append(append([]string{}, pending...), line)
			pending = nil
			inTask = true
			continue
		}
		switch {
		case level == 0:
			if !strings.Contains(line, subSkill) {
				pre = append(pre, line)
			}
		case inTask && h > 0 && h < level:
			inTask = false
			pending = append(pending, line)
		case inTask:
			cur = append(cur, line)
		default:
			pending = append(pending, line)
		}
	}
	if len(p.Tasks) == 0 {
		title := p.Title
		if title == "" {
			title = "the plan"
		}
		body := strings.TrimSpace(text)
		return Plan{Title: title, Tasks: []Task{{N: 1, Title: title, Body: body, head: -1}}}
	}
	cur = append(cur, pending...)
	flush()
	if p.Title == "" {
		p.Title = "the plan"
	}
	p.Preamble = strings.TrimSpace(strings.Join(pre, "\n"))
	return p
}

// Brief is the text a builder is given for Tasks[i]: the plan's title, which
// task this is, the shared preamble, and that task alone. Tasks are counted by
// position, the number the build loop uses, whatever the plan's headings say.
func (p Plan) Brief(i int) string { return p.brief(i, p.Tasks[i].Body) }

// brief renders Tasks[i] with body in place of its own text.
func (p Plan) brief(i int, body string) string {
	t := p.Tasks[i]
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nTask %d of %d: %s\n\n", p.Title, i+1, len(p.Tasks), t.Title)
	if p.Preamble != "" {
		b.WriteString(p.Preamble)
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}
