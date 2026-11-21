package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

func initBaselineRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := gitCmd(root, "init", "-q"); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := gitCmd(root, "config", "user.email", "t@t"); err != nil {
		t.Fatalf("git config email: %v %s", err, out)
	}
	if out, err := gitCmd(root, "config", "user.name", "t"); err != nil {
		t.Fatalf("git config name: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".tandem-relay/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitCmd(root, "add", "."); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if out, err := gitCmd(root, "commit", "-qm", "init"); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	if err := os.MkdirAll(paths.Relay(root), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGateBaselineCheckCachesByTreeHashAndCommand(t *testing.T) {
	root := initBaselineRepo(t)
	relay := paths.Relay(root)
	counter := filepath.Join(t.TempDir(), "runs")
	cmd := "printf x >> " + counter + " && echo baseline-ok"

	// --cached-only before any run reports miss and does not run the command.
	rc, out, errOut := runCapture(t, "gate", "baseline-check", "--root", root, "--relay", relay, "--command", cmd, "--cached-only", "--fields")
	if rc != exitcode.OK {
		t.Fatalf("cached-only miss: rc %d err %s", rc, errOut)
	}
	f := fields(out)
	if len(f) < 5 || f[0] != "miss" || f[2] == "" {
		t.Fatalf("cached-only miss fields: %q", f)
	}
	if _, err := os.Stat(counter); !os.IsNotExist(err) {
		t.Fatalf("cached-only ran the check command")
	}

	// First real call runs the check, records baseline-check.json, returns fresh.
	rc, out, errOut = runCapture(t, "gate", "baseline-check", "--root", root, "--relay", relay, "--command", cmd, "--fields")
	if rc != exitcode.OK {
		t.Fatalf("fresh run: rc %d err %s", rc, errOut)
	}
	f = fields(out)
	if len(f) < 5 || f[0] != "fresh" || f[1] != "0" || f[2] == "" || !strings.Contains(f[4], "baseline-ok") {
		t.Fatalf("fresh run fields: %q", f)
	}
	tree1 := f[2]
	if b, err := os.ReadFile(counter); err != nil || string(b) != "x" {
		t.Fatalf("expected 1 run, got %q (%v)", string(b), err)
	}

	// Second call on the same tree reuses the cached result without re-running.
	rc, out, errOut = runCapture(t, "gate", "baseline-check", "--root", root, "--relay", relay, "--command", cmd, "--fields")
	if rc != exitcode.OK {
		t.Fatalf("cached run: rc %d err %s", rc, errOut)
	}
	f = fields(out)
	if len(f) < 5 || f[0] != "cached" || f[1] != "0" || f[2] != tree1 || !strings.Contains(f[4], "baseline-ok") {
		t.Fatalf("cached run fields: %q", f)
	}
	if b, err := os.ReadFile(counter); err != nil || string(b) != "x" {
		t.Fatalf("cache hit should not re-run command, got %q", string(b))
	}

	// --cached-only on a warm cache returns cached.
	rc, out, errOut = runCapture(t, "gate", "baseline-check", "--root", root, "--relay", relay, "--command", cmd, "--cached-only", "--fields")
	if rc != exitcode.OK {
		t.Fatalf("cached-only hit: rc %d err %s", rc, errOut)
	}
	if f := fields(out); len(f) < 5 || f[0] != "cached" || f[1] != "0" {
		t.Fatalf("cached-only hit fields: %q", f)
	}

	// Modifying a file changes the tree hash and triggers a fresh check.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out, errOut = runCapture(t, "gate", "baseline-check", "--root", root, "--relay", relay, "--command", cmd, "--fields")
	if rc != exitcode.OK {
		t.Fatalf("after edit: rc %d err %s", rc, errOut)
	}
	f = fields(out)
	if len(f) < 5 || f[0] != "fresh" || f[2] == tree1 {
		t.Fatalf("expected fresh run with new tree hash, got %q (old %q)", f, tree1)
	}
	if b, err := os.ReadFile(counter); err != nil || string(b) != "xx" {
		t.Fatalf("expected 2 runs after tree change, got %q", string(b))
	}

	// Changing the check command invalidates the cache on the same tree.
	cmd2 := "echo fail-msg && exit 7"
	rc, out, errOut = runCapture(t, "gate", "baseline-check", "--root", root, "--relay", relay, "--command", cmd2)
	if rc != exitcode.OK {
		t.Fatalf("failing check: rc %d err %s", rc, errOut)
	}
	var got baselineCheckOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v (%q)", err, out)
	}
	if got.Status != "fresh" || got.Exit != 7 || !strings.Contains(got.Summary, "fail-msg") {
		t.Fatalf("failing check out: %+v", got)
	}
}
