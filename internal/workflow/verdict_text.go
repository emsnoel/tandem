package workflow

// The verdict a reviewer writes by hand.
//
// A reviewer writes its verdict as plain text, and tandem builds the JSON. A
// finding's message often quotes code, a plan step or an error, and a reviewer
// that writes JSON by hand forgets to escape those quotes (#60). In this text
// nothing needs escaping:
//
//	status: changes_requested
//
//	finding: high open internal/app/main.go:12-14
//	Step 5 requires: "Lint must pass." Any text, on as many lines as needed.
//
//	finding: low open README.md
//	Another message.

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// VerdictFormat is how a verdict is written, as the review prompt shows it.
const VerdictFormat = "status: OUTCOME\n\n" +
	"finding: SEVERITY DISPOSITION PATH[:START[-END]]\n" +
	"The message, on as many lines as you need. Write quotes, backslashes and code exactly as they are."

var verdictLocation = regexp.MustCompile(`^(.+?):([0-9]+)(?:-([0-9]+))?$`)

// ParseVerdict reads a verdict written as text. It checks only the text's
// shape and names the line it cannot read; ValidateReview judges the content.
func ParseVerdict(text []byte) (Verdict, error) {
	v := Verdict{SchemaVersion: 1, Findings: []Finding{}}
	bad := func(n int, format string, a ...any) (Verdict, error) {
		return Verdict{}, fmt.Errorf("%w: verdict line %d: %s", ErrIncompleteEvidence, n, fmt.Sprintf(format, a...))
	}
	lines := strings.Split(string(bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return Verdict{}, fmt.Errorf("%w: the verdict is empty", ErrIncompleteEvidence)
	}
	first := strings.TrimSpace(lines[i])
	switch {
	case strings.HasPrefix(first, "{"):
		return bad(i+1, "the verdict is plain text, not JSON. Write it as:\n\n%s", VerdictFormat)
	case !strings.HasPrefix(first, "status:"):
		return bad(i+1, "the verdict starts with status: and then passed, changes_requested or failed")
	}
	if v.Status = strings.TrimSpace(strings.TrimPrefix(first, "status:")); v.Status == "" {
		return bad(i+1, "status: names no outcome")
	}
	var message []string
	flush := func() {
		if len(v.Findings) > 0 {
			v.Findings[len(v.Findings)-1].Message = strings.TrimSpace(strings.Join(message, "\n"))
		}
		message = nil
	}
	for i++; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "finding:") {
			if len(v.Findings) == 0 && line != "" {
				return bad(i+1, "text before the first finding: line. Start each finding with finding: SEVERITY DISPOSITION PATH")
			}
			message = append(message, lines[i])
			continue
		}
		flush()
		f, err := parseFindingHeader(strings.TrimSpace(strings.TrimPrefix(line, "finding:")))
		if err != nil {
			return bad(i+1, "%v", err)
		}
		v.Findings = append(v.Findings, f)
	}
	flush()
	return v, nil
}

// parseFindingHeader reads "SEVERITY DISPOSITION PATH[:START[-END]]". The path
// is the rest of the line, so it may hold spaces.
func parseFindingHeader(header string) (Finding, error) {
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return Finding{}, fmt.Errorf("finding: needs SEVERITY DISPOSITION PATH[:START[-END]], not %q", header)
	}
	f := Finding{Severity: fields[0], Disposition: fields[1]}
	rest := strings.TrimSpace(header[len(fields[0]):])
	rest = strings.TrimSpace(rest[len(fields[1]):])
	m := verdictLocation.FindStringSubmatch(rest)
	if m == nil {
		f.Path = rest
		return f, nil
	}
	f.Path = m[1]
	for _, n := range []struct {
		text string
		to   **int
	}{{m[2], &f.StartLine}, {m[3], &f.EndLine}} {
		if n.text == "" {
			continue
		}
		v, err := strconv.Atoi(n.text)
		if err != nil || v < 1 {
			return Finding{}, fmt.Errorf("%s is not a line number", n.text)
		}
		*n.to = &v
	}
	return f, nil
}
