package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// OffMainInput reports whether a pane's screen shows something other than its
// own conversation's input, so typed text would not start a turn there. why
// describes the screen for a person.
//
// Herdr reads each of these as idle or working, never blocked:
//
//   - claude, a subagent's screen: input goes to that subagent.
//   - claude, the agent list or /tasks: Enter picks a row and the text stays
//     unsent.
//   - claude, an earlier prompt recalled with Up: new text joins it.
//   - codex, a sub-agent's thread: input is disabled.
//   - codex, the agent command center: Enter opens another session.
//   - opencode, a child session: the text is dropped.
//   - copilot, a subagent's screen: input goes to that subagent.
//   - copilot, its task list, focused sidebar and Sessions tab: single keys
//     act there (x kills a task, enter opens one). Herdr reads the Sessions
//     tab as working.
//   - copilot, a GitHub tab (Issues, Pull requests, Gists).
//
// A screen that matches nothing is not off: a wrong refusal holds a delivery
// until a person looks.
func OffMainInput(kind, screen string) (why string, off bool) {
	lines := strings.Split(screen, "\n")
	switch kind {
	case "claude":
		return claudeOffMain(lines)
	case "codex":
		for _, l := range lines {
			t := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(t, "Agent command center"):
				return "the agent command center, where Enter opens a session", true
			case strings.HasPrefix(t, "›") && strings.Contains(t, "Viewing sub-agent"):
				return "a sub-agent's thread, whose input is disabled", true
			}
		}
	case "opencode":
		for _, l := range lines {
			if opencodeChildFooter.MatchString(l) {
				return "a child session, which cannot be prompted", true
			}
		}
	case "copilot":
		return copilotOffMain(bottomLines(lines, 2))
	}
	return "", false
}

// copilotOffMain reads Copilot's footer, which names the keys of whatever
// has them: its last line, or the one above a dialog's bottom border. The tab
// bar is not read: it also shows above the main conversation.
func copilotOffMain(bottom []string) (string, bool) {
	footer := strings.Join(bottom, "\n")
	switch {
	case strings.Contains(footer, "Esc back to tasks"):
		return "a subagent's screen, whose input goes to that subagent", true
	case strings.Contains(footer, "enter details"), strings.Contains(footer, "esc close"):
		// "esc close" is any dialog's; the task list shows it with no rows.
		return "its task list or another dialog, where single keys act (x kills a task)", true
	case strings.Contains(footer, "x close · n new"), strings.Contains(footer, "n new session"):
		return "its session list, where single keys act (enter opens a session)", true
	case len(bottom) > 0 && strings.TrimSpace(bottom[0]) == "tab switch tabs":
		return "a GitHub tab, not the conversation", true
	}
	return "", false
}

// bottomLines is the last n lines with text on them.
func bottomLines(lines []string, n int) []string {
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	return out
}

// opencodeChildFooter is the footer of an opencode child session: its agent,
// its place among its siblings, and the key back to the parent.
var opencodeChildFooter = regexp.MustCompile(`\(\d+ of \d+\).*\bParent\b`)

// claudeOffMain reads Claude Code's input box: a line starting ❯ under a rule.
// The main conversation's rule is plain; a subagent's screen titles it with
// the subagent's task and addresses the input to it ("Message @name…").
// "↑/↓ to select" at the bottom means a list has the keys: the agent list
// below the box, or /tasks, which shows no box at all.
func claudeOffMain(lines []string) (string, bool) {
	for _, l := range bottomLines(lines, 4) {
		if strings.HasPrefix(strings.TrimSpace(l), "↑/↓ to select") {
			if why, off := claudeSubagentBox(lines); off {
				return why, true
			}
			return "a list that takes Enter in place of the input (the agent list or /tasks)", true
		}
	}
	return claudeSubagentBox(lines)
}

// claudeSubagentBox reports a subagent's screen by the title on the rule
// above the input box. The rule is also titled while Up recalls an earlier
// prompt ("History 11/11"), whose text is then in the input.
func claudeSubagentBox(lines []string) (string, bool) {
	box := -1
	for i := len(lines) - 1; i > 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "❯") && isRule(lines[i-1]) {
			box = i
			break
		}
	}
	if box < 0 {
		return "", false
	}
	switch title := ruleTitle(lines[box-1]); {
	case strings.HasPrefix(title, "History "):
		return "an earlier prompt recalled into its input, which typed text would be sent with", true
	case title != "":
		return fmt.Sprintf("the screen of subagent %q, whose input goes to that subagent", title), true
	}
	return "", false
}

// isRule reports whether a line is a horizontal rule, titled or not.
func isRule(l string) bool {
	return strings.HasPrefix(strings.TrimSpace(l), "───")
}

// ruleTitle is the text set into a rule, "" for a plain one.
func ruleTitle(l string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "─"))
}
