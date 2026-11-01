package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func rolesRelay(t *testing.T, body string) string {
	t.Helper()
	relay := t.TempDir()
	if body != "" {
		if err := os.MkdirAll(filepath.Join(relay, "build"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(relay, "build", "build.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return relay
}

func TestBuildRolesFields(t *testing.T) {
	relay := rolesRelay(t, `{"plan":"/p.md","builder":"b1","reviewer":"r1","planner":"pl","tasks":[]}`)
	out := captureStdout(t, func() int { return cmdBuild([]string{"roles", "--relay", relay}) })
	if out != "/p.md\x00b1\x00r1\x00pl\x00" {
		t.Errorf("got %q", out)
	}
}

func TestBuildRolesBuilderDefault(t *testing.T) {
	relay := rolesRelay(t, `{"plan":"/p.md","reviewer":"r1"}`)
	out := captureStdout(t, func() int { return cmdBuild([]string{"roles", "--relay", relay}) })
	if out != "/p.md\x00builder\x00r1\x00\x00" {
		t.Errorf("got %q", out)
	}
}

func TestBuildRolesMissingReviewer(t *testing.T) {
	relay := rolesRelay(t, `{"plan":"/p.md","builder":"b1"}`)
	out := captureStdout(t, func() int { return cmdBuild([]string{"roles", "--relay", relay}) })
	if out != "/p.md\x00b1\x00\x00\x00" {
		t.Errorf("got %q", out)
	}
}

// Review focus 5: no readable record prints nothing and fails, so the caller's
// own refusal or fallback runs.
func TestBuildRolesMissingFile(t *testing.T) {
	out, rc := captureStdoutRC(t, func() int { return cmdBuild([]string{"roles", "--relay", rolesRelay(t, "")}) })
	if rc != exitcode.Internal || out != "" {
		t.Errorf("rc %d, out %q", rc, out)
	}
}

func TestBuildRolesNotJSON(t *testing.T) {
	out, rc := captureStdoutRC(t, func() int { return cmdBuild([]string{"roles", "--relay", rolesRelay(t, "nope")}) })
	if rc != exitcode.Internal || out != "" {
		t.Errorf("rc %d, out %q", rc, out)
	}
}
