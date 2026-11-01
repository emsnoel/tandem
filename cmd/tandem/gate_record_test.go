package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

const recordID = "20260101-000000-1-1"

func recordArgs(relay, plan, verdict, measured string) []string {
	return []string{"gate", "record", "--relay", relay, "--id", recordID, "--plan", plan, "--verdict", verdict,
		"--kind", "codex", "--autonomy", "auto", "--reviewer-kind", "claude", "--reviewer-model", "",
		"--reviewer-measured=" + measured}
}

// Review focus 1: the sidecar is byte for byte what tandem-verdict's jq wrote
// on main, so `gate review` reads old and new records alike.
func TestGateRecordBytes(t *testing.T) {
	relay := t.TempDir()
	plan := "/r/a \"b\" & <c> é\nd.md"
	if rc, _, errOut := runCapture(t, recordArgs(relay, plan, "clean", "false")...); rc != exitcode.OK {
		t.Fatalf("rc %d: %s", rc, errOut)
	}
	got, err := os.ReadFile(filepath.Join(relay, "reviews", recordID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "plan": "/r/a \"b\" & <c> é\nd.md",
  "verdict": "clean",
  "kind": "codex",
  "autonomy": "auto",
  "reviewed_by": "reviewer",
  "reviewer_kind": "claude",
  "reviewer_model": "",
  "reviewer_model_measured": false,
  "override_reason": ""
}
`
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestGateRecordThenReview(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "p.md")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRelayFile(t, root, "builder.json", `{"kind":"codex","autonomy":"auto"}`)
	writeRelayFile(t, root, "reviewer.json", `{"kind":"claude","name":"reviewer","autonomy":"auto","model_measured":false}`)
	writeRelayFile(t, root, "reviews/"+recordID+".plan.md", "# plan\n")
	if rc, _, errOut := runCapture(t, recordArgs(paths.Relay(root), plan, "clean", "false")...); rc != exitcode.OK {
		t.Fatalf("record rc %d: %s", rc, errOut)
	}
	if rc, out, _ := runCapture(t, "gate", "review", "--plan", plan, "--root", root); rc != exitcode.OK || out != recordID+"\n" {
		t.Errorf("review rc %d out %q", rc, out)
	}
}

func TestGateRecordRefuses(t *testing.T) {
	for name, args := range map[string][]string{
		"an id with a path": {"--id", "../x"},
		"verdict maybe":     {"--verdict", "maybe"},
		"measured yes":      {"--reviewer-measured=yes"},
		"no plan":           {"--plan", ""},
		"no kind":           {"--kind", ""},
		"no autonomy":       {"--autonomy", ""},
	} {
		t.Run(name, func(t *testing.T) {
			relay := t.TempDir()
			full := append(recordArgs(relay, "/p.md", "clean", "true"), args...)
			if rc, _, _ := runCapture(t, full...); rc != exitcode.Usage {
				t.Errorf("rc %d, want usage", rc)
			}
			if _, err := os.Stat(filepath.Join(relay, "reviews")); !os.IsNotExist(err) {
				t.Errorf("a refused record wrote reviews/: %v", err)
			}
		})
	}
}

func TestRelayWriteVerdict(t *testing.T) {
	mb := t.TempDir()
	msg := "the reviewer reviewed /p.md\n\n  review: /r.md"
	raw, m := relayFile(t, filepath.Join(mb, "q", "id6.json"), "write", "verdict", "--mailbox", mb, "--id", "id6",
		"--from", "reviewer", "--to", "planner", "--message", msg, "--plan", "/p.md", "--review-path", "/r.md")
	want := []string{"id", "kind", "from", "to", "question", "message", "plan", "review_path", "created"}
	if got := keysInOrder(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
	if m["kind"] != "verdict" || m["question"] != "verdict" || m["from"] != "reviewer" || m["to"] != "planner" ||
		m["message"] != msg || m["plan"] != "/p.md" || m["review_path"] != "/r.md" {
		t.Errorf("got %v", m)
	}
}

// Review focus 2: the bridge reads a queued verdict back as kind verdict,
// with its message unchanged.
func TestRelayWriteVerdictReadsBack(t *testing.T) {
	mb := t.TempDir()
	msg := "line one\nline two & <three>"
	relayFile(t, filepath.Join(mb, "q", "id7.json"), "write", "verdict", "--mailbox", mb, "--id", "id7",
		"--from", "reviewer", "--to", "planner", "--message", msg, "--plan", "/p.md", "--review-path", "/r.md")
	rc, out, _ := runCapture(t, "relay", "read", "--file", filepath.Join(mb, "q", "id7.json"))
	f := fields(out)
	if rc != exitcode.OK || len(f) != 11 || f[0] != "id7" || f[1] != "verdict" || f[2] != "reviewer" ||
		f[3] != "verdict" || f[4] != msg {
		t.Errorf("rc %d fields %q", rc, f)
	}
}
