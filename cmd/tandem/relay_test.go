package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// relayFile runs a relay verb that must succeed and returns the file it wrote,
// raw and decoded.
func relayFile(t *testing.T, path string, args ...string) (string, map[string]any) {
	t.Helper()
	captureStdout(t, func() int { return cmdRelay(args) })
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %s", b)
	}
	return string(b), m
}

func keysInOrder(raw string) []string {
	var keys []string
	for _, m := range regexp.MustCompile(`(?m)^  "([a-z_]+)":`).FindAllStringSubmatch(raw, -1) {
		keys = append(keys, m[1])
	}
	return keys
}

func TestRelayWriteHandbackShape(t *testing.T) {
	mb := t.TempDir()
	raw, m := relayFile(t, filepath.Join(mb, "q", "id2.json"), "write", "handback", "--mailbox", mb, "--id", "id2",
		"--from", "builder", "--to", "planner", "--message", "done", "--report", "/r.md", "--plan", "/p.md")
	want := []string{"id", "kind", "from", "to", "question", "message", "report_path", "plan", "created"}
	if got := keysInOrder(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
	if m["kind"] != "handback" || m["question"] != "handback" || m["to"] != "planner" || m["message"] != "done" {
		t.Errorf("got %v", m)
	}
}

// Review focus 3: a question has no kind, so the bridge reads it as one.
func TestRelayWriteQuestionHasNoKind(t *testing.T) {
	mb := t.TempDir()
	raw, _ := relayFile(t, filepath.Join(mb, "q", "id3.json"), "write", "question", "--mailbox", mb, "--id", "id3",
		"--from", "builder", "--to", "planner", "--question", "why?")
	if got, want := keysInOrder(raw), []string{"id", "from", "to", "question", "created"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
}

// Review focus 1: text comes back unchanged, and & < stay literal in the file.
func TestRelayWriteKeepsText(t *testing.T) {
	mb := t.TempDir()
	msg := "line one\n\tsaid \"hi\" & <left>"
	raw, m := relayFile(t, filepath.Join(mb, "q", "id4.json"), "write", "handback", "--mailbox", mb, "--id", "id4",
		"--from", "b", "--to", "p", "--message", msg, "--report", "/r", "--plan", "/p")
	if m["message"] != msg || !strings.Contains(raw, "& <left>") {
		t.Errorf("got %q in %s", m["message"], raw)
	}
}

func TestRelayWriteCreatedFormat(t *testing.T) {
	mb := t.TempDir()
	_, m := relayFile(t, filepath.Join(mb, "q", "id5.json"), "write", "question", "--mailbox", mb, "--id", "id5",
		"--from", "b", "--to", "p", "--question", "q")
	if c, _ := m["created"].(string); !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`).MatchString(c) {
		t.Errorf("created %q", c)
	}
}

func TestRelayWriteLeavesNoTmp(t *testing.T) {
	mb := t.TempDir()
	relayFile(t, filepath.Join(mb, "q", "id6.json"), "write", "question", "--mailbox", mb, "--id", "id6",
		"--from", "b", "--to", "p", "--question", "q")
	if _, err := os.Stat(filepath.Join(mb, "q", "id6.json.tmp")); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind")
	}
}

func TestRelayLogShape(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tandem-log")
	raw, m := relayFile(t, filepath.Join(dir, "id7.json"), "log", "--dir", dir, "--id", "id7",
		"--from", "builder", "--to", "planner", "--question", "q?", "--answer", "a.")
	if got, want := keysInOrder(raw), []string{"id", "ts", "from", "to", "question", "answer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
	if m["question"] != "q?" || m["answer"] != "a." {
		t.Errorf("got %v", m)
	}
}
