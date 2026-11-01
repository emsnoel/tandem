package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// relayRead runs `tandem relay read` on a file holding body and returns the
// fields it printed and its exit code.
func relayRead(t *testing.T, body string) ([]string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return relayReadPath(t, path)
}

func relayReadPath(t *testing.T, path string) ([]string, int) {
	t.Helper()
	out, rc := captureStdoutRC(t, func() int { return cmdRelay([]string{"read", "--file", path}) })
	if out == "" {
		return nil, rc
	}
	if !strings.HasSuffix(out, "\x00") {
		t.Fatalf("output does not end in NUL: %q", out)
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00"), rc
}

// Field order: id, kind, from, question, message, created, task, report_path,
// stuck, review, verdict.
func TestRelayReadQuestion(t *testing.T) {
	got, rc := relayRead(t, `{"id":"q1","from":"builder","to":"planner","question":"what now?","created":"2026-11-01T10:00:00Z"}`)
	want := []string{"q1", "question", "builder", "what now?", "", "2026-11-01T10:00:00Z", "", "", "false", "false", ""}
	if rc != exitcode.OK || !reflect.DeepEqual(got, want) {
		t.Errorf("rc %d, got %q, want %q", rc, got, want)
	}
}

func TestRelayReadTask(t *testing.T) {
	got, _ := relayRead(t, `{"id":"t1","kind":"task","from":"builder","task":3,"stuck":true,"report_path":"r.md","verdict":"clean"}`)
	want := []string{"t1", "task", "builder", "", "", "", "3", "r.md", "true", "false", "clean"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The question path and the task path default an empty sender differently, so
// relay read leaves it empty.
func TestRelayReadFromHasNoDefault(t *testing.T) {
	got, _ := relayRead(t, `{"id":"q1","question":"hi"}`)
	if len(got) != 11 || got[2] != "" {
		t.Errorf("got %q", got)
	}
}

// As jq's `//` did, null and false take the default.
func TestRelayReadNullAndFalseTakeDefaults(t *testing.T) {
	got, _ := relayRead(t, `{"id":"x","kind":null,"from":false,"task":null,"stuck":null,"review":false,"verdict":false}`)
	want := []string{"x", "question", "", "", "", "", "", "", "false", "false", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Review focus 1: a number or an object prints as its JSON text, as tostring.
func TestRelayReadNonStringsPrintAsJSON(t *testing.T) {
	got, _ := relayRead(t, `{"id":7,"kind":"task","task":"3","question":{"a":1},"message":[1, 2]}`)
	if len(got) != 11 {
		t.Fatalf("got %q", got)
	}
	if got[0] != "7" || got[6] != "3" || got[3] != `{"a":1}` || got[4] != "[1,2]" {
		t.Errorf("got %q", got)
	}
}

// Review focus 3: separators inside a field do not move the fields after it.
func TestRelayReadKeepsFieldsInPlace(t *testing.T) {
	got, _ := relayRead(t, `{"id":"h1","kind":"handback","message":"a\tb\nc\u001fd\u0000e","report_path":"r.md"}`)
	if len(got) != 11 {
		t.Fatalf("got %d fields: %q", len(got), got)
	}
	if got[4] != "a\tb\nc\x1fde" || got[7] != "r.md" {
		t.Errorf("got %q", got)
	}
}

// Review focus 1: what is not a JSON object prints nothing.
func TestRelayReadNotAnObject(t *testing.T) {
	for _, body := range []string{`[1,2]`, `not json`, `null`, `"s"`} {
		if got, rc := relayRead(t, body); rc != exitcode.Internal || got != nil {
			t.Errorf("%s: rc %d, got %q", body, rc, got)
		}
	}
}

func TestRelayReadMissingFile(t *testing.T) {
	if got, rc := relayReadPath(t, filepath.Join(t.TempDir(), "gone.json")); rc != exitcode.Internal || got != nil {
		t.Errorf("rc %d, got %q", rc, got)
	}
}

func TestRelayReadNeedsFile(t *testing.T) {
	if _, rc := captureStdoutRC(t, func() int { return cmdRelay([]string{"read"}) }); rc != exitcode.Usage {
		t.Errorf("rc %d", rc)
	}
}

// What the relay writers write, relay read reads back with its types.
func TestRelayReadWhatRelayWriteWrote(t *testing.T) {
	mb := t.TempDir()
	captureStdout(t, func() int {
		return cmdRelay([]string{"write", "task", "--mailbox", mb, "--id", "t9", "--from", "reviewer",
			"--task", "2", "--report", "r & <s>.md", "--plan", "p.md", "--review", "--verdict", "objections"})
	})
	got, rc := relayReadPath(t, filepath.Join(mb, "q", "t9.json"))
	if rc != exitcode.OK || len(got) != 11 {
		t.Fatalf("rc %d, got %q", rc, got)
	}
	want := []string{"t9", "task", "reviewer", "task", "", got[5], "2", "r & <s>.md", "false", "true", "objections"}
	if !reflect.DeepEqual(got, want) || got[5] == "" {
		t.Errorf("got %q, want %q", got, want)
	}
}
