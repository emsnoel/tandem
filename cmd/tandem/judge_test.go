package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/decision/jev"
	"github.com/emsnoel/tandem/internal/triage"
)

// fakeJudge stands in for the Jev client: it counts calls and returns what
// it is told to, so the tests can see whether a request was made at all.
type fakeJudge struct {
	calls   int
	answers map[string]decision.Answer
	err     error
}

type fakeJev = fakeJudge

func (f *fakeJudge) Ask(context.Context, string, any, map[string]decision.Question) (map[string]decision.Answer, error) {
	f.calls++
	return f.answers, f.err
}

func useFakeJudge(t *testing.T, f *fakeJudge) {
	t.Helper()
	orig := newJevClient
	newJevClient = func(string, time.Duration) jevClient { return f }
	t.Cleanup(func() { newJevClient = orig })
}

// cleanBatch is a well-formed answer set that clears every skip threshold.
func cleanBatch() map[string]decision.Answer {
	return map[string]decision.Answer{
		triage.QImplements:  {Type: "noul", Noul: 0.97},
		triage.QScope:       {Type: "noul", Noul: 0.01},
		triage.QDestructive: {Type: "noul", Noul: 0.0},
		triage.QPriority:    {Type: "score", Score: 0.2},
		triage.QEvidence:    {Type: "noul", Noul: 0.9},
	}
}

func confidentAnswers() map[string]decision.Answer { return cleanBatch() }

func TestJudgeTaskReturnsAVerdictAndRecordsTheTrueCheck(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-test")
	root := t.TempDir()
	c := &fakeJev{answers: confidentAnswers()}
	v, err := judgeTask(context.Background(), c, judgeRequest{
		ID: "j1", TaskID: "1", TaskBrief: "add greet", Diff: "+greet",
		CheckCommandExists: true, CheckPassed: true,
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if !v.WouldSkip || v.RecordID != "j1" {
		t.Fatalf("got %+v", v)
	}
	rec := readJudgeRecord(t, root, "j1")
	if !rec.CheckPassed || rec.Verdict != "clean" {
		t.Fatalf("record %+v", rec)
	}
}

func TestJudgeTaskNeverSkipsOnARedCheck(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-test")
	c := &fakeJev{answers: confidentAnswers()}
	v, err := judgeTask(context.Background(), c, judgeRequest{
		ID: "j2", CheckCommandExists: true, CheckPassed: false,
	}, t.TempDir())
	if err != nil || v.WouldSkip {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

func TestJudgeTaskReportsWhyThereIsNoVerdict(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	root := t.TempDir()
	_, err := judgeTask(context.Background(), &fakeJev{}, judgeRequest{ID: "j3"}, root)
	if err == nil || !strings.Contains(err.Error(), "TYPESAFE_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	if rec := readJudgeRecord(t, root, "j3"); rec.Verdict != "unavailable" || !strings.Contains(rec.Unavailable, "TYPESAFE_API_KEY") {
		t.Fatalf("record %+v", rec)
	}
}

// Missing credentials must produce a durable unavailable record without a call.
func TestJudgeTaskWithoutAKeyMakesNoRequest(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	c := &fakeJudge{answers: cleanBatch()}
	root := t.TempDir()
	_, err := judgeTask(context.Background(), c, judgeRequest{ID: "no-key"}, root)
	if err == nil || !strings.Contains(err.Error(), "subscription") {
		t.Fatalf("err = %v", err)
	}
	if c.calls != 0 {
		t.Fatalf("no key, so no request; got %d calls", c.calls)
	}
	if rec := readJudgeRecord(t, root, "no-key"); rec.Verdict != "unavailable" || rec.Unavailable != err.Error() {
		t.Fatalf("record %+v", rec)
	}
}

func TestJudgeTaskRecordsUnavailableProviderAnswers(t *testing.T) {
	cases := []struct {
		name   string
		client *fakeJudge
		reason string
	}{
		{"unauthorized", &fakeJudge{err: fmt.Errorf("%w (HTTP 401)", jev.ErrUnauthorized)}, "subscription"},
		{"provider failure", &fakeJudge{err: errors.New("jev returned 529")}, "the provider call failed (jev returned 529)"},
		{"malformed answer", &fakeJudge{answers: map[string]decision.Answer{}}, "the response could not be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TYPESAFE_API_KEY", "ts-test")
			root := t.TempDir()
			_, err := judgeTask(context.Background(), tc.client, judgeRequest{ID: "unavailable"}, root)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v, want %q", err, tc.reason)
			}
			if tc.name == "unauthorized" && strings.Contains(err.Error(), "the provider call failed") {
				t.Fatalf("a rejected key is a subscription problem: %v", err)
			}
			if rec := readJudgeRecord(t, root, "unavailable"); rec.Verdict != "unavailable" || rec.Unavailable != err.Error() {
				t.Fatalf("record %+v", rec)
			}
		})
	}
}

// A diff can carry a secret a plan would not. The judge path must redact it
// before anything leaves the machine.
func TestJudgeRedactsTheDiffBeforeSending(t *testing.T) {
	got := buildState("brief", "export TYPESAFE_API_KEY=ts-live-abc123def456\n", false, "")
	if strings.Contains(got.Diff, "ts-live-abc123def456") {
		t.Fatal("the diff reached state unredacted")
	}
	if !strings.Contains(got.Diff, "TYPESAFE_API_KEY") {
		t.Error("redaction should blank the value, not erase the evidence that a key was there")
	}
}

// Truncation has to be visible in the state, because an overlarge diff is
// where the judgment is least reliable -- see internal/triage.MaxDiffBytes.
func TestBuildStateMarksAnOverlargeDiffAsTruncated(t *testing.T) {
	big := strings.Repeat("x", triage.MaxDiffBytes+1)
	got := buildState("brief", big, true, "ok")
	if !got.Truncated {
		t.Fatal("an oversized diff must set Truncated")
	}
	if len(got.Diff) > triage.MaxDiffBytes {
		t.Fatalf("diff not bounded: %d bytes, budget is %d", len(got.Diff), triage.MaxDiffBytes)
	}
	small := buildState("brief", "a small diff", true, "ok")
	if small.Truncated {
		t.Error("a diff inside the budget must not be marked truncated")
	}
}
