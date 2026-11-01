package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// logDir writes each body to DIR/name.
func logDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func askRecord(i int) string {
	return fmt.Sprintf(`{"id":"r%d","ts":"t%d","from":"builder","to":"planner","question":"q%d","answer":"a%d"}`, i, i, i, i)
}

func history(t *testing.T, args ...string) (int, []string) {
	t.Helper()
	rc, out, _ := runCapture(t, append([]string{"relay", "history"}, args...)...)
	if out == "" {
		return rc, nil
	}
	return rc, fields(out)
}

// questions picks field 7 (question) of each 8-field record.
func questions(t *testing.T, f []string) []string {
	t.Helper()
	if len(f)%8 != 0 {
		t.Fatalf("%d fields is not whole records: %q", len(f), f)
	}
	var qs []string
	for i := 0; i < len(f); i += 8 {
		qs = append(qs, f[i+6])
	}
	return qs
}

func TestRelayHistoryOldestFirst(t *testing.T) {
	dir := logDir(t, map[string]string{"20260101-2.json": askRecord(2), "20260101-1.json": askRecord(1), "20260101-3.json": askRecord(3)})
	rc, f := history(t, "--dir", dir)
	if rc != exitcode.OK || !reflect.DeepEqual(questions(t, f), []string{"q1", "q2", "q3"}) {
		t.Fatalf("rc %d %q", rc, f)
	}
	want := []string{filepath.Join(dir, "20260101-1.json"), askRecord(1), "r1", "t1", "builder", "planner", "q1", "a1"}
	if !reflect.DeepEqual(f[:8], want) {
		t.Errorf("first record %q, want %q", f[:8], want)
	}
}

func TestRelayHistoryLimit(t *testing.T) {
	dir := logDir(t, map[string]string{"1.json": askRecord(1), "2.json": askRecord(2), "3.json": askRecord(3)})
	for limit, want := range map[string][]string{
		"2": {"q2", "q3"},
		"5": {"q1", "q2", "q3"},
		"0": {"q1", "q2", "q3"},
	} {
		if rc, f := history(t, "--dir", dir, "--limit", limit); rc != exitcode.OK || !reflect.DeepEqual(questions(t, f), want) {
			t.Errorf("limit %s: rc %d %q", limit, rc, f)
		}
	}
	for _, bad := range []string{"-1", "x"} {
		if rc, _ := history(t, "--dir", dir, "--limit", bad); rc != exitcode.Usage {
			t.Errorf("limit %s: rc %d, want usage", bad, rc)
		}
	}
}

func TestRelayHistorySkipsJudge(t *testing.T) {
	dir := logDir(t, map[string]string{"judge-1.json": `{"id":"j"}`, "2.json": askRecord(2), "notes.txt": "x"})
	if rc, f := history(t, "--dir", dir); rc != exitcode.OK || !reflect.DeepEqual(questions(t, f), []string{"q2"}) {
		t.Errorf("rc %d %q", rc, f)
	}
}

// Review focus 3: a broken file keeps its place and leaves the others whole.
func TestRelayHistoryBrokenFile(t *testing.T) {
	dir := logDir(t, map[string]string{"1.json": askRecord(1), "2.json": "{not json", "3.json": askRecord(3), "4.json": "[1]"})
	rc, f := history(t, "--dir", dir)
	if rc != exitcode.OK || len(f) != 32 {
		t.Fatalf("rc %d %q", rc, f)
	}
	for _, i := range []int{1, 3} {
		if want := append([]string{filepath.Join(dir, fmt.Sprintf("%d.json", i+1))}, make([]string, 7)...); !reflect.DeepEqual(f[i*8:i*8+8], want) {
			t.Errorf("broken record %d: %q", i, f[i*8:i*8+8])
		}
	}
	if f[6] != "q1" || f[22] != "q3" {
		t.Errorf("good records %q", f)
	}
}

func TestRelayHistoryNullAndNumber(t *testing.T) {
	dir := logDir(t, map[string]string{"1.json": "{\n  \"id\": \"r1\",\n  \"ts\": 5,\n  \"answer\": null\n}\n"})
	rc, f := history(t, "--dir", dir)
	if rc != exitcode.OK || len(f) != 8 || f[1] != `{"id":"r1","ts":5,"answer":null}` || f[3] != "5" || f[7] != "" || f[4] != "" {
		t.Errorf("rc %d %q", rc, f)
	}
}

func TestRelayHistoryEmpty(t *testing.T) {
	if rc, f := history(t, "--dir", filepath.Join(t.TempDir(), "missing")); rc != exitcode.OK || f != nil {
		t.Errorf("missing dir: rc %d %q", rc, f)
	}
	dir := logDir(t, map[string]string{"judge-1.json": `{}`})
	if rc, f := history(t, "--dir", dir); rc != exitcode.OK || f != nil {
		t.Errorf("only judge files: rc %d %q", rc, f)
	}
	if rc, _ := history(t); rc != exitcode.Usage {
		t.Errorf("no --dir: rc %d, want usage", rc)
	}
}
