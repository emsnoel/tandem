package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func line(n int) *int { return &n }

// A reviewer writes the verdict as text, so a message that quotes code, a plan
// step or an error is taken as written. Nothing in it needs escaping (#60).
func TestParseVerdictTakesMessagesAsWritten(t *testing.T) {
	text := "status: changes_requested\n\n" +
		"finding: high open internal/app/main.go:12-14\n" +
		"Task 1, Step 5 explicitly requires: \"Lint must pass with no icon warnings.\"\n" +
		"The check prints `C:\\path\\to\\icon` and {\"a\":1}.\n\n" +
		"finding: low open docs/a b.md:3\n  Indented text keeps its inner spacing.\n" +
		"finding: info fixed README.md\nWhole file.\n"
	got, err := ParseVerdict([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	want := Verdict{SchemaVersion: 1, Status: ReviewStatusChangesRequested, Findings: []Finding{
		{Severity: SeverityHigh, Path: "internal/app/main.go", Disposition: DispositionOpen, StartLine: line(12), EndLine: line(14),
			Message: "Task 1, Step 5 explicitly requires: \"Lint must pass with no icon warnings.\"\nThe check prints `C:\\path\\to\\icon` and {\"a\":1}."},
		{Severity: SeverityLow, Path: "docs/a b.md", Disposition: DispositionOpen, StartLine: line(3), Message: "Indented text keeps its inner spacing."},
		{Severity: SeverityInfo, Path: "README.md", Disposition: DispositionFixed, Message: "Whole file."},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	raw, _ := json.Marshal(got)
	if err := ValidateVerdictRaw(raw); err != nil {
		t.Fatalf("the parsed verdict does not validate: %v\n%s", err, raw)
	}
}

func TestParseVerdictPassedHasNoFindings(t *testing.T) {
	got, err := ParseVerdict([]byte("\r\nstatus: passed\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if string(raw) != `{"schema_version":1,"status":"passed","findings":[]}` {
		t.Fatalf("got %s", raw)
	}
}

func TestParseVerdictNamesTheLineItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"empty":                  {"  \n", "the verdict is empty"},
		"json":                   {`{"schema_version":1,"status":"passed","findings":[]}`, "line 1: the verdict is plain text, not JSON"},
		"no status":              {"finding: high open a.go\nx\n", "line 1: the verdict starts with status:"},
		"text before a finding":  {"status: failed\nit is broken\n", "line 2: text before the first finding:"},
		"short header":           {"status: failed\nfinding: high a.go\nx\n", "line 2: finding: needs SEVERITY DISPOSITION PATH"},
		"bad line range":         {"status: failed\nfinding: high open a.go:0\nx\n", "line 2: 0 is not a line number"},
		"a second status":        {"status: failed\nstatus: passed\n", "line 2: text before the first finding:"},
		"status with no outcome": {"status:\n", "line 1: status: names no outcome"},
	} {
		if _, err := ParseVerdict([]byte(tc.text)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}
