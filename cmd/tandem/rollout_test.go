package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

var placeholder = regexp.MustCompile(`^[A-Z]+$`)

// Every command the operations guide shows is accepted by the parser: the verb
// exists and its flags are known. The commands run against an empty directory
// and state home, so they refuse for want of state; a malformed shape would
// exit 2 instead.
func TestOperationsCommandsMatchUsage(t *testing.T) {
	raw, err := os.ReadFile("../../guide/durable-runner-operations.md")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "state-home")
	t.Setenv("TANDEM_STATE_HOME", home)
	// The samples that read a run are durable-run commands, so DIR carries a
	// durable marker; the state behind it does not exist, so they refuse.
	if err := os.MkdirAll(paths.Relay(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"engine_version":2,"protocol_version":2,"run_id":"` + strings.Repeat("a", 32) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(paths.Relay(dir), "active-run.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	subst := map[string]string{"DIR": dir, "PATH": filepath.Join(dir, "out"), "ID": strings.Repeat("a", 32), "N": "1"}
	seen := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "    tandem ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		for i, f := range fields {
			if placeholder.MatchString(f) {
				v, ok := subst[f]
				if !ok {
					t.Fatalf("%q: no value for the placeholder %s", line, f)
				}
				fields[i] = v
			}
		}
		seen++
		rc, _, errOut := runCapture(t, fields...)
		if rc == exitcode.Usage {
			t.Errorf("%q is not accepted by the parser: %s", strings.TrimSpace(line), errOut)
		}
	}
	if seen < 15 {
		t.Errorf("only %d documented commands were checked; the guide's samples were not found", seen)
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("checking the samples created the state home")
	}
}
