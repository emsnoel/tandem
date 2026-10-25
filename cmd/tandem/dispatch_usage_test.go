package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestDispatchRejectsRemovedBuilderJudgingCommand(t *testing.T) {
	if rc := run([]string{"decision", "judge"}); rc != exitcode.Usage {
		t.Errorf("removed judging subcommand exited %d, want usage exit %d", rc, exitcode.Usage)
	}
	// Supply the former command's required arguments too: its old handler
	// already returned Usage when called without them.
	if rc := run([]string{"decision", "judge", "--in", "missing.json", "--out", "-", "--root", t.TempDir()}); rc != exitcode.Usage {
		t.Errorf("removed judging subcommand still accepts arguments: exit %d, want %d", rc, exitcode.Usage)
	}
	script := "../../bin/tandem-" + "judge"
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Errorf("removed delegate must not exist: stat %s: %v", script, err)
	}
}

// coreSubcommands is a copy of run()'s switch for usage output, so check it
// against the switch read from main.go.
func TestUsageListsEveryVerbTheSwitchAnswers(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	// The switch arms are `case "verb":`, one verb per arm in this file.
	re := regexp.MustCompile(`(?m)^\s*case "([a-z]+)":`)
	var inSwitch []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		inSwitch = append(inSwitch, m[1])
	}
	if len(inSwitch) == 0 {
		t.Fatal("found no `case \"verb\":` arms in main.go; this test has stopped testing anything")
	}

	listed := map[string]bool{}
	for _, v := range coreSubcommands {
		listed[v] = true
	}

	var missing []string
	for _, v := range inSwitch {
		if !listed[v] {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("run() answers %s but coreSubcommands does not list them, so `tandem` with no arguments hides them",
			strings.Join(missing, ", "))
	}

	answered := map[string]bool{}
	for _, v := range inSwitch {
		answered[v] = true
	}
	for _, v := range coreSubcommands {
		if !answered[v] {
			t.Errorf("coreSubcommands advertises %q but run() has no case for it", v)
		}
	}
}

// A core subcommand must never share a name with a bin/tandem-<verb> script.
// dispatch.go falls through to scripts only for verbs run() does not answer, so
// a shared name would shadow the script and the two spellings would differ.
func TestNoCoreSubcommandShadowsADelegateScript(t *testing.T) {
	entries, err := os.ReadDir("../../bin")
	if err != nil {
		t.Fatalf("reading bin/: %v", err)
	}
	core := map[string]bool{}
	for _, v := range coreSubcommands {
		core[v] = true
	}
	found := false
	for _, e := range entries {
		verb, ok := strings.CutPrefix(e.Name(), "tandem-")
		if !ok {
			continue
		}
		found = true
		if core[verb] {
			t.Errorf("bin/%s and the core subcommand %q share a name; `tandem %s` would reach the binary and never the script, so the two spellings would not do the same thing",
				e.Name(), verb, verb)
		}
	}
	if !found {
		t.Fatal("found no bin/tandem-* scripts; this test has stopped testing anything")
	}
}
