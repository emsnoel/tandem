package agent

import "strings"

// agyOffMain reads agy's screen (1.5.14). Every agy dialog names its keys on
// a line that starts "↑/↓ Navigate", just above the footer. Only the bottom
// lines count, because the transcript above the input can quote that text.
// "esc to cancel" is not a marker: agy's footer shows it while it works.
// What the dialog is comes from its body and its marker line.
func agyOffMain(lines []string) (string, bool) {
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
