package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDelegate puts an executable named tandem-<verb> in dir.
func fakeDelegate(t *testing.T, dir, verb string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, delegatePrefix+verb)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

// A verb is a lowercase word and nothing else. The rejections matter more than
// the acceptances: exec.LookPath resolves anything holding a separator as a
// path, so a verb that reaches it unchecked is a way to run an arbitrary file.
func TestValidVerb(t *testing.T) {
	for _, verb := range []string{"pair", "handoff", "unpair", "a", "x2", "a-b", "report-metadata"} {
		if !validVerb(verb) {
			t.Errorf("validVerb(%q) = false, want true", verb)
		}
	}
	bad := []string{
		"",                      // no verb at all
		"../pair",               // parent directory
		"/bin/sh",               // absolute path
		"a/b",                   // any separator
		"Pair",                  // uppercase
		"-r",                    // leads with a dash, reads as a flag
		"2fast",                 // leads with a digit
		"pair;rm -rf /",         // shell punctuation
		"pair ",                 // trailing space
		"pa$(whoami)",           // substitution
		strings.Repeat("a", 33), // longer than any real verb
	}
	for _, verb := range bad {
		if validVerb(verb) {
			t.Errorf("validVerb(%q) = true, want false", verb)
		}
	}
}

// resolveDelegate finds an executable delegate on PATH and nothing else.
func TestResolveDelegate(t *testing.T) {
	dir := t.TempDir()
	fakeDelegate(t, dir, "probe", 0o755)
	fakeDelegate(t, dir, "unreadable", 0o644) // present but not executable
	t.Setenv("PATH", dir)

	if got := resolveDelegate("probe"); got == "" {
		t.Error("resolveDelegate(\"probe\") = \"\", want the temp-dir path")
	}
	if got := resolveDelegate("unreadable"); got != "" {
		t.Errorf("resolveDelegate(\"unreadable\") = %q, want \"\" — a non-executable file is not a command", got)
	}
	if got := resolveDelegate("absent"); got != "" {
		t.Errorf("resolveDelegate(\"absent\") = %q, want \"\"", got)
	}
	// The guard holds even when a matching file really is sitting there: this
	// resolves only via delegatePrefix, never as a path.
	if got := resolveDelegate("../" + filepath.Base(dir) + "/tandem-probe"); got != "" {
		t.Errorf("resolveDelegate(path) = %q, want \"\" — a verb is never a path", got)
	}
}

// delegateVerbs reports what is reachable now, deduplicated across PATH
// entries, and skips what cannot be run.
func TestDelegateVerbs(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	fakeDelegate(t, first, "pair", 0o755)
	fakeDelegate(t, second, "pair", 0o755) // same verb, shadowed
	fakeDelegate(t, second, "handoff", 0o755)
	fakeDelegate(t, second, "notexec", 0o644)
	if err := os.Mkdir(filepath.Join(second, delegatePrefix+"adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{first, second, filepath.Join(second, "missing")}, string(os.PathListSeparator)))

	got := delegateVerbs()
	want := []string{"handoff", "pair"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("delegateVerbs() = %v, want %v", got, want)
	}
}

// An unknown verb with no script behind it is a usage error, not a crash, and
// it must not be mistaken for a gate refusal or any other coded exit.
func TestRunUnknownSubcommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	stderr := os.Stderr
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()

	if got := run([]string{"nosuchverb"}); got != 2 {
		t.Errorf("run([nosuchverb]) = %d, want 2 (exitcode.Usage)", got)
	}
	if got := run(nil); got != 2 {
		t.Errorf("run(nil) = %d, want 2 (exitcode.Usage)", got)
	}
}
