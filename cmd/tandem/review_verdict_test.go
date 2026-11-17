package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestReviewVerdictRejectedBeforeQueueWrites(t *testing.T) {
	d := durableRepo(t)
	d.setProtocol(t, 3)
	for _, tc := range []struct{ body, reason string }{
		{`{"schema_version":1,"status":"passed","findings":[]}`, "plain text, not JSON"},
		{"status: changes_requested\n", "changes_requested"},
		{"status: great\n", `status "great" is not a reviewer outcome`},
		{"status: failed\nfinding: high open\nx\n", "verdict line 2"},
		{"status: failed\nfinding: high open ../secret\nx\n", "finding 1"},
	} {
		beforeQ, _ := os.ReadDir(filepath.Join(d.dir, "q"))
		beforePayload, _ := os.ReadDir(filepath.Join(d.dir, "payloads"))
		file := filepath.Join(t.TempDir(), "verdict.txt")
		if err := os.WriteFile(file, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		rc, _, stderr := d.newEvent(t, "review_result", "--payload", file)
		if rc != exitcode.Usage || !strings.Contains(stderr, tc.reason) {
			t.Fatalf("exit %d stderr %q; want usage and %q", rc, stderr, tc.reason)
		}
		afterQ, _ := os.ReadDir(filepath.Join(d.dir, "q"))
		afterPayload, _ := os.ReadDir(filepath.Join(d.dir, "payloads"))
		if len(afterQ) != len(beforeQ) || len(afterPayload) != len(beforePayload) {
			t.Fatal("invalid verdict wrote queued data")
		}
	}
}

// The reviewer writes text and the queue carries the JSON tandem built from
// it, with a quoted message exactly as written (#60).
func TestReviewVerdictTextIsQueuedAsJSON(t *testing.T) {
	d := durableRepo(t)
	d.setProtocol(t, 3)
	file := filepath.Join(t.TempDir(), "verdict.txt")
	if err := os.WriteFile(file, []byte("status: changes_requested\n\nfinding: high open a.go:3\nIt requires: \"quoted text\" and \\n.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, got, stderr := d.newEvent(t, "review_result", "--payload", file)
	if rc != exitcode.OK {
		t.Fatalf("valid verdict: %d %s", rc, stderr)
	}
	if env := d.envelopeOf(t, got); env.SchemaVersion != 3 {
		t.Fatalf("version %d", env.SchemaVersion)
	}
	b, _ := os.ReadFile(got["payload"].(string))
	want := `{"schema_version":1,"status":"changes_requested","findings":[{"severity":"high","path":"a.go","disposition":"open","message":"It requires: \"quoted text\" and \\n.","start_line":3,"end_line":null}]}`
	if string(b) != want {
		t.Fatalf("queued payload\n%s\nwant\n%s", b, want)
	}
}
