package agent

import (
	"fmt"
	"strings"
)

// OffMainInput reports whether a pane's screen shows something other than its
// own conversation's input, so typed text would not start a turn there. why
// describes the screen for a person.
//
// Herdr reads each of these as idle or working, never blocked (but for the
// startup prompts named so a refusal can say what they are: Codex's update
// prompt, Claude Code's folder-trust prompt and Bypass Permissions warning,
// and Copilot's folder-trust prompt):
//
//   - claude, any dialog: its last line offers "Esc to cancel".
//   - claude, a subagent's screen: input goes to that subagent.
//   - claude, the agent list or /tasks: Enter picks a row and the text stays
//     unsent.
//   - claude, an earlier prompt recalled with Up: new text joins it.
//   - codex, a sub-agent's thread: input is disabled.
//   - codex, the agent command center: Enter opens another session.
//   - codex, the "Hooks need review" prompt at startup and the hooks screens
//     behind it: Enter picks a row, t trusts every hook.
//   - copilot, any dialog: its footer offers "esc to cancel".
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
		return codexOffMain(lines)
	case "copilot":
		return copilotOffMain(lines)
	}
	return "", false
}

// codexOffMain reads Codex's screen. A dialog names its keys on the last line
// and always offers Esc ("enter confirm · esc skip"); the main input's last
// line never does. Only that line marks a dialog, because the transcript above
// the input can quote any of this text. What the dialog is comes from its body.
func codexOffMain(lines []string) (string, bool) {
	has := func(prefix string) bool { return hasLine(lines, prefix) }
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "Agent command center"):
			return "the agent command center, where Enter opens a session", true
		case strings.HasPrefix(t, "›") && strings.Contains(t, "Viewing sub-agent"):
			return "a sub-agent's thread, whose input is disabled", true
		}
	}
	bottom := bottomLines(lines, 1)
	if len(bottom) == 0 {
		return "", false
	}
	keys := strings.TrimSpace(bottom[0])
	if !strings.Contains(keys, "· esc ") {
		return "", false
	}
	switch {
	case has("Hooks need review"):
		return "Codex's \"Hooks need review\" prompt, which waits for an answer", true
	case has("Update available"):
		return "Codex's \"Update available\" prompt, which waits for an answer", true
	case strings.Contains(keys, "toggle"):
		return "a hook list, where Space and Enter turn a hook on or off", true
	case has("Lifecycle hooks"):
		return "its hooks screen, where single keys act (t trusts every hook)", true
	}
	return fmt.Sprintf("a dialog that has the keys (%q)", keys), true
}

// copilotOffMain reads Copilot's footer, which names the keys of whatever
// has them: its last line, or the one above a dialog's bottom border. The tab
// bar is not read: it also shows above the main conversation. A dialog's
// footer offers "esc to cancel"; what the dialog is comes from its body.
func copilotOffMain(lines []string) (string, bool) {
	bottom := bottomLines(lines, 2)
	footer := strings.Join(bottom, "\n")
	switch {
	case strings.Contains(footer, "esc to cancel") && hasLine(lines, "│ Confirm folder trust"):
		return "Copilot's folder-trust prompt, which waits for an answer", true
	case strings.Contains(footer, "esc to cancel"):
		for _, l := range bottom {
			if strings.Contains(l, "esc to cancel") {
				return fmt.Sprintf("a dialog that has the keys (%q)", strings.Trim(strings.TrimSpace(l), "│ ")), true
			}
		}
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

// hasLine reports whether a line starts with prefix, ignoring indentation.
func hasLine(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return true
		}
	}
	return false
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

// claudeOffMain reads Claude Code's input box: a line starting ❯ under a rule.
// The main conversation's rule is plain; a subagent's screen titles it with
// the subagent's task and addresses the input to it ("Message @name…").
// "↑/↓ to select" at the bottom means a list has the keys: the agent list
// below the box, or /tasks, which shows no box at all.
func claudeOffMain(lines []string) (string, bool) {
	if why, off := claudeDialog(lines); off {
		return why, true
	}
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

// claudeDialog reports a dialog by its last line, "Enter to confirm · Esc to
// cancel". Only that line counts, because the transcript above the input can
// quote any of this text. What the dialog is comes from its body.
func claudeDialog(lines []string) (string, bool) {
	bottom := bottomLines(lines, 1)
	if len(bottom) == 0 || !strings.Contains(bottom[0], "Esc to cancel") {
		return "", false
	}
	switch {
	case hasLine(lines, "Quick safety check:"):
		return "Claude Code's folder-trust prompt, which waits for an answer", true
	case hasLine(lines, "WARNING: Claude Code running in Bypass Permissions mode"):
		return "Claude Code's Bypass Permissions warning, which waits for an answer", true
	}
	return fmt.Sprintf("a dialog that has the keys (%q)", strings.TrimSpace(bottom[0])), true
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
