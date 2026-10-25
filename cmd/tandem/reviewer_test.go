package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// tandem reviewer show answers for every script: a complete record as fields,
// and a fixed code each for none and incomplete, with nothing on stdout.
func TestReviewerShow(t *testing.T) {
	root := t.TempDir()
	if rc, out, _ := runCapture(t, "reviewer", "show", "--root", root); rc != exitcode.NoReviewer || out != "" {
		t.Fatalf("no record: rc %d out %q", rc, out)
	}
	for _, body := range []string{`{"kind":"claude"}`, `{"name":"reviewer"}`, `not json`} {
		writeRelayFile(t, root, "reviewer.json", body)
		if rc, out, _ := runCapture(t, "reviewer", "show", "--root", root); rc != exitcode.ReviewerIncomplete || out != "" {
			t.Fatalf("%s: rc %d out %q", body, rc, out)
		}
	}
	writeRelayFile(t, root, "reviewer.json", `{"kind":"codex","name":"critic","autonomy":"auto","model_measured":false}`)
	if rc, out, _ := runCapture(t, "reviewer", "show", "--root", root); rc != exitcode.OK || out != "codex\tcritic\tfalse\t\n" {
		t.Fatalf("unmeasured: rc %d out %q", rc, out)
	}
	writeRelayFile(t, root, "reviewer.json", `{"kind":"codex","name":"critic","model":"gpt-x","autonomy":"auto","model_measured":true}`)
	if rc, out, _ := runCapture(t, "reviewer", "show", "--root", root); rc != exitcode.OK || out != "codex\tcritic\ttrue\tgpt-x\n" {
		t.Fatalf("measured: rc %d out %q", rc, out)
	}
	rc, out, _ := runCapture(t, "reviewer", "show", "--root", root, "--json")
	if got := oneJSON(t, out); rc != exitcode.OK || got["name"] != "critic" || got["model"] != "gpt-x" || got["autonomy"] != "auto" || got["model_measured"] != true {
		t.Fatalf("json: rc %d %v", rc, got)
	}
	if rc, out, _ := runCapture(t, "reviewer", "show", "--relay", filepath.Join(root, ".tandem-relay")); rc != exitcode.OK || out != "codex\tcritic\ttrue\tgpt-x\n" {
		t.Fatalf("--relay: rc %d out %q", rc, out)
	}
	if rc, _, _ := runCapture(t, "reviewer", "show", "--root", root, "--relay", root); rc != exitcode.Usage {
		t.Fatalf("--root with --relay: rc %d", rc)
	}
	if rc, _, errOut := runCapture(t, "reviewer", "list"); rc != exitcode.Usage || !strings.Contains(errOut, "reviewer show") {
		t.Fatalf("unknown verb: rc %d %q", rc, errOut)
	}
}
