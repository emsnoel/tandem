package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/doctor"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// tandem-pair prints whatever --problems prints, so a pass with warnings shows
// them and a clean pass shows nothing.
func TestDoctorProblemsPrintsOnlyWarnAndFail(t *testing.T) {
	root := doctorRepo(t, `{"builder": {"kind": "codex"}}`)
	t.Setenv("HERDR_ENV", "")
	out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--problems"}) })
	if rc != exitcode.DoctorFailed {
		t.Fatalf("got exit %d, want 30\n%s", rc, out)
	}
	for _, want := range []string{"\n  warn  ", "\n  FAIL  ", "failures"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "\n  ok    ") {
		t.Errorf("--problems printed an ok line:\n%s", out)
	}
}

func TestDoctorProblemsSilentWhenClean(t *testing.T) {
	root := healthyRepo(t)
	out, rc := captureStdoutRC(t, func() int {
		return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude", "--problems"})
	})
	if rc != exitcode.OK || out != "" {
		t.Fatalf("got exit %d and output %q, want 0 and nothing", rc, out)
	}
}

// Review focus 4: scripts read the JSON counts, so --problems leaves it whole.
func TestDoctorProblemsLeavesJSONWhole(t *testing.T) {
	root := healthyRepo(t)
	t.Setenv("HERDR_ENV", "")
	out, _ := captureStdoutRC(t, func() int {
		return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude", "--json", "--problems"})
	})
	var doc struct {
		Results  []doctor.Result `json:"results"`
		Warnings int             `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	oks := 0
	for _, r := range doc.Results {
		if r.Status == doctor.OK {
			oks++
		}
	}
	if oks == 0 || doc.Warnings != 1 {
		t.Fatalf("got %d ok results and %d warnings, want ok results kept and 1 warning", oks, doc.Warnings)
	}
}
