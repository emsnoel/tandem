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
		{`{"status":"passed","findings":[]}`, "schema_version"},
		{`{"schema_version":1,"status":"changes_requested","findings":[]}`, "changes_requested"},
		{`{"schema_version":1,"status":"passed","findings":[],"job_id":"` + strings.Repeat("a", 33) + `"}`, "32 lowercase hex"},
	} {
		beforeQ, _ := os.ReadDir(filepath.Join(d.dir, "q"))
		beforePayload, _ := os.ReadDir(filepath.Join(d.dir, "payloads"))
		file := filepath.Join(t.TempDir(), "verdict.json")
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
	file := filepath.Join(t.TempDir(), "verdict.json")
	os.WriteFile(file, []byte(`{"schema_version":1,"status":"passed","findings":[]}`), 0o644)
	if rc, got, stderr := d.newEvent(t, "review_result", "--payload", file); rc != exitcode.OK {
		t.Fatalf("valid verdict: %d %s", rc, stderr)
	} else if env := d.envelopeOf(t, got); env.SchemaVersion != 3 {
		t.Fatalf("version %d", env.SchemaVersion)
	}
}
