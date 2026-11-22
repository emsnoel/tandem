package agent

import "strings"

// agyOffMain reads agy's screen (1.5.14). Every agy dialog names its keys on
// a line that starts "↑/↓ Navigate", just above the footer. Only the bottom
// lines count, because the transcript above the input can quote that text.
// "esc to cancel" is not a marker: agy's footer shows it while it works.
// What the dialog is comes from its body and its marker line.
func agyOffMain(lines []string) (string, bool) {
	if agySurvey(lines) {
		return "agy's feedback survey, which takes whatever is typed next", true
	}
	marker := ""
	for _, l := range bottomLines(lines, 4) {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "↑/↓ Navigate") {
			marker = t
			break
		}
	}
	if marker == "" {
		return "", false
	}
	switch {
	case hasLine(lines, "Do you trust the contents of this project?"):
		return "agy's folder-trust prompt, which waits for an answer", true
	case hasLine(lines, "Requesting permission for:"):
		return "a permission prompt, where Enter approves the command", true
	case strings.Contains(marker, "tab Complete"):
		return "the slash-command menu, where Enter runs the highlighted command", true
	}
	return "an agy dialog, where Enter picks a row", true
}

// agySurvey reports whether lines show agy's feedback survey. After some turns
// agy 1.5.14 draws it in place of the input box, just above the footer:
//
//	How's the CLI experience so far? Help us improve:
//	[1] Good  [2] Fine  [3] Bad  [0] Skip
//
// Herdr reports the pane idle, and whatever is typed answers the survey and
// is dropped: a /new typed there started no conversation. Only the bottom
// lines count, because the transcript above can quote the text.
func agySurvey(lines []string) bool {
	question, options := false, false
	for _, l := range bottomLines(lines, 3) {
		t := strings.TrimSpace(l)
		question = question || strings.HasPrefix(t, "How's the CLI experience so far?")
		options = options || (strings.HasPrefix(t, "[1] Good") && strings.Contains(t, "[0] Skip"))
	}
	return question && options
}

// agySurveySkip is the survey's own key for "[0] Skip".
const agySurveySkip = "0"

// agyFreshScreen reports whether lines show agy 1.5.14 at a fresh, empty
// conversation: the welcome banner at the top, an empty (non-command) input
// box framed by horizontal rules at the bottom, no dialog or active turn in
// the footer, and no conversation transcript between the banner and the input.
func agyFreshScreen(lines []string) bool {
	if _, off := agyOffMain(lines); off {
		return false
	}
	for _, l := range bottomLines(lines, 2) {
		if strings.Contains(l, "esc to cancel") {
			return false
		}
	}
	banner := -1
	for i, l := range lines {
		if strings.Contains(l, "Antigravity CLI") {
			banner = i
			break
		}
	}
	if banner < 0 {
		return false
	}
	box := -1
	for i := len(lines) - 2; i > banner; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), ">") && isRule(lines[i-1]) && isRule(lines[i+1]) {
			box = i
			break
		}
	}
	if box < 0 {
		return false
	}
	// An unsubmitted slash command (such as "> /new") means the input has not cleared.
	after := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[box]), ">"))
	if strings.HasPrefix(after, "/") {
		return false
	}
	for i := banner + 1; i < box-1; i++ {
		t := strings.TrimSpace(lines[i])
		if isRule(lines[i]) || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "○ ") || strings.HasPrefix(t, "● ") {
			return false
		}
	}
	return true
}
