package check

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, command string, extra []string, timeout time.Duration) (Result, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "logs", "check.log")
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := Run(ctx, dir, command, extra, log)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("log not written: %v", err)
	}
	return r, string(b)
}

func TestRunRecordsAPassAndItsOutput(t *testing.T) {
	r, log := run(t, "echo all good", nil, 5*time.Second)
	if r.Exit != 0 || r.TimedOut {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(log, "all good") {
		t.Errorf("log = %q", log)
	}
}

func TestRunRecordsTheCommandsOwnExitCode(t *testing.T) {
	r, _ := run(t, "echo broken >&2; exit 3", nil, 5*time.Second)
	if r.Exit != 3 {
		t.Fatalf("exit = %d, want 3", r.Exit)
	}
}

func TestRunPassesExtraArgumentsToTheCommand(t *testing.T) {
	_, log := run(t, "printf '[%s]'", []string{"--no-watchman", "a b"}, 5*time.Second)
	if !strings.Contains(log, "[--no-watchman][a b]") {
		t.Errorf("log = %q", log)
	}
}

func TestRunStopsAHungCheck(t *testing.T) {
	r, _ := run(t, "sleep 30", nil, 300*time.Millisecond)
	if !r.TimedOut || r.Exit != TimeoutExit {
		t.Fatalf("got %+v, want a timeout with exit %d", r, TimeoutExit)
	}
}

func TestSummaryOfAPassIsShort(t *testing.T) {
	log := strings.Repeat("PASS src/a.test.ts\n", 200) + "Tests: 12 passed\n"
	s := Summary(Result{Exit: 0, Duration: 12 * time.Second, Log: "/r/check.log"}, log)
	if !strings.Contains(s, "check: exit 0 in 12s") || !strings.Contains(s, "log: /r/check.log") {
		t.Errorf("summary = %q", s)
	}
	if strings.Count(s, "\n") > 8 {
		t.Errorf("a passing summary should be a few lines, got:\n%s", s)
	}
}

func TestSummaryOfAFailureShowsTheFailingLinesAndTheTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "PASS suite %d\n", i)
	}
	b.WriteString("FAIL src/broken.test.ts\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "noise %d\n", i)
	}
	b.WriteString("Tests: 1 failed, 99 passed\n")
	s := Summary(Result{Exit: 1, Duration: time.Second, Log: "x"}, b.String())
	if !strings.Contains(s, "FAIL src/broken.test.ts") || !strings.Contains(s, "Tests: 1 failed, 99 passed") {
		t.Errorf("summary lacks the failure or the tail:\n%s", s)
	}
	if strings.Contains(s, "noise 10\n") {
		t.Errorf("summary carries lines from the middle of the log:\n%s", s)
	}
}

func TestSummaryCapsTheFailingLines(t *testing.T) {
	log := strings.Repeat("error: something\n", 500)
	s := Summary(Result{Exit: 1, Log: "x"}, log)
	if n := strings.Count(s, "error: something"); n > maxMatches+tailLines {
		t.Errorf("summary has %d matching lines, cap is %d", n, maxMatches+tailLines)
	}
}

func TestSummarySaysWhenTheCheckTimedOut(t *testing.T) {
	s := Summary(Result{Exit: TimeoutExit, TimedOut: true, Duration: time.Minute, Log: "x"}, "")
	if !strings.Contains(s, "timed out") {
		t.Errorf("summary = %q", s)
	}
}

func writeLog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A check that prints one enormous line must not take the summary with it, and
// the full log stays where it was.
func TestCheckSummaryBoundsSingleHugeLine(t *testing.T) {
	huge := strings.Repeat("x", 20<<20)
	for name, body := range map[string]string{
		"failure on a later line": huge + "\nFAIL: the marker\nTests: 1 failed\n",
		"failure at the huge end": huge + " FAIL: boom\nTests: 1 failed\n",
		"nothing but the line":    huge,
	} {
		log := writeLog(t, body)
		s, err := SummarizeFile(Result{Exit: 1, Duration: time.Second, Log: log}, "sha256:abc123", MaxSummaryBytes)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(s) > MaxSummaryBytes {
			t.Errorf("%s: summary is %d bytes, over %d", name, len(s), MaxSummaryBytes)
		}
		if !strings.Contains(s, "sha256:abc123") {
			t.Errorf("%s: the summary lost its log reference:\n%.300s", name, s)
		}
		if info, err := os.Stat(log); err != nil || info.Size() != int64(len(body)) {
			t.Errorf("%s: the full log changed: %v", name, err)
		}
	}
	log := writeLog(t, huge+"\nFAIL: the marker\nTests: 1 failed\n")
	s, _ := SummarizeFile(Result{Exit: 1, Log: log}, "ref", MaxSummaryBytes)
	if !strings.Contains(s, "FAIL: the marker") || !strings.Contains(s, "Tests: 1 failed") {
		t.Errorf("the failure after a huge line is missing:\n%.400s", s)
	}
	log = writeLog(t, huge+" FAIL: boom\n")
	if s, _ = SummarizeFile(Result{Exit: 1, Log: log}, "ref", MaxSummaryBytes); !strings.Contains(s, "FAIL: boom") {
		t.Errorf("the end of a huge line was dropped:\n%.400s", s)
	}
	if !strings.Contains(s, "bytes omitted") {
		t.Errorf("a shortened line must say so:\n%.400s", s)
	}
}

func TestSummarizeFileBoundsManyLongFailingLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "error %d: %s\n", i, strings.Repeat("detail ", 600))
	}
	s, err := SummarizeFile(Result{Exit: 1, Log: writeLog(t, b.String())}, "ref", MaxSummaryBytes)
	if err != nil || len(s) > MaxSummaryBytes {
		t.Fatalf("len %d err %v", len(s), err)
	}
	if !strings.Contains(s, "ref") || !strings.Contains(s, "last lines") {
		t.Errorf("summary:\n%.500s", s)
	}
	small, _ := SummarizeFile(Result{Exit: 1, Log: writeLog(t, strings.Repeat("error: x\n", 50))}, "ref", 400)
	if len(small) > 400 || !strings.Contains(small, "truncated") {
		t.Errorf("a tighter budget (%d bytes) must truncate with a marker:\n%s", len(small), small)
	}
}

func TestSummarizeFileKeepsStdoutAndStderrInTheOrderWritten(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "job.log")
	cmd := `for i in 1 2 3; do echo "out $i"; echo "err $i" >&2; done; echo "FAIL: done" >&2; exit 2`
	r, err := Run(context.Background(), dir, cmd, nil, log)
	if err != nil || r.Exit != 2 {
		t.Fatalf("run: %+v %v", r, err)
	}
	s, err := SummarizeFile(r, "ref", MaxSummaryBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"out 1", "err 1", "out 2", "err 2", "out 3", "err 3", "FAIL: done"}
	at := 0
	for _, w := range want {
		i := strings.Index(s[at:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order after offset %d in:\n%s", w, at, s)
		}
		at += i + len(w)
	}
}

func TestSummarizeFileOfAPassIsShortAndSaysTimeouts(t *testing.T) {
	log := writeLog(t, strings.Repeat("PASS a\n", 100)+"Tests: 12 passed\n")
	s, _ := SummarizeFile(Result{Exit: 0, Duration: 12 * time.Second, Log: log}, "ref", MaxSummaryBytes)
	if !strings.Contains(s, "check: exit 0 in 12s") || !strings.Contains(s, "Tests: 12 passed") || strings.Count(s, "\n") > 8 {
		t.Errorf("summary:\n%s", s)
	}
	s, _ = SummarizeFile(Result{Exit: TimeoutExit, TimedOut: true, Log: writeLog(t, "")}, "ref", MaxSummaryBytes)
	if !strings.Contains(s, "timed out") {
		t.Errorf("summary:\n%s", s)
	}
	if _, err := SummarizeFile(Result{Exit: 1, Log: filepath.Join(t.TempDir(), "absent.log")}, "ref", MaxSummaryBytes); err == nil {
		t.Error("a missing log was summarized as empty")
	}
}

func TestSummarizeFileRedactsSecretsInTheLinesItShows(t *testing.T) {
	log := writeLog(t, "starting\nexport API_TOKEN=sk-live-abcdef123456\nFAIL: Authorization: Bearer abcdefghijklmnop\n")
	s, err := SummarizeFile(Result{Exit: 1, Log: log}, "ref", MaxSummaryBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-live-abcdef123456", "abcdefghijklmnop"} {
		if strings.Contains(s, secret) {
			t.Errorf("the summary leaks %q:\n%s", secret, s)
		}
	}
	if !strings.Contains(s, "[REDACTED]") || !strings.Contains(s, "API_TOKEN") {
		t.Errorf("summary:\n%s", s)
	}
}
