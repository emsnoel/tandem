package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Each CLI's log gives back what the agent replied, whole; prompts are not
// replies. Codex and Copilot conversations are found by the prompt's marker.
func TestAgentRepliesReadsEachKindsLog(t *testing.T) {
	t.Parallel()
	const marker = "49f1d12fa5479d76940d891b9c6917c4"
	now := time.Now()
	from := now.Add(-time.Hour)
	codex := t.TempDir()
	writeLog(t, filepath.Join(codex, now.Format("2006/01/02"), "rollout-2026-11-20T11-45-21-rev-1.jsonl"),
		`{"type":"session_meta","payload":{"id":"rev-1"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"job `+marker+`"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex says"}]}}`)
	claude := t.TempDir()
	writeLog(t, filepath.Join(claude, "-repo", "c0ffee-1.jsonl"),
		`{"type":"user","message":{"role":"user","content":"job `+marker+`"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"claude says"}]}}`)
	copilot := t.TempDir()
	writeLog(t, filepath.Join(copilot, "session-state", copilotSession, "events.jsonl"),
		copilotStart(copilotSession), copilotUserLine(t, "job "+marker),
		`{"type":"assistant.message","data":{"content":""}}`,
		`{"type":"assistant.message","data":{"content":"copilot says"}}`)
	agy := t.TempDir()
	writeAgyDB(t, agy, agySession, []agyStepRow{
		{stepType: agyStepUserInput, payload: agyUserStepBlob("job " + marker)},
		{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("hm", "", "")},
		{stepType: agyStepPlannerResponse, payload: agyPlannerStepBlob("", "agy says", "agy says")},
	}, nil)
	src := AgentUsageSource{CodexSessions: codex, ClaudeProjects: claude, CopilotHome: copilot, AgyHome: agy}

	for _, c := range []struct{ kind, session, want string }{
		{"codex", "", "codex says\n"},
		{"codex", "rev-1", "codex says\n"},
		{"claude", "c0ffee-1", "claude says\n"},
		{"claude", "", ""},
		{"copilot", "launch-id", "copilot says\n"},
		{"agy", agySession, "agy says\n"},
		{"agy", "", "agy says\n"},
	} {
		got, err := src.AgentReplies(context.Background(), c.kind, c.session, marker, from, now)
		if err != nil || got != c.want {
			t.Errorf("%s %q: got %q, %v; want %q", c.kind, c.session, got, err, c.want)
		}
	}
	if _, err := src.AgentReplies(context.Background(), "opencode", "", marker, from, now); err == nil {
		t.Error("a kind with no log reader was read")
	}
}

// A Claude reviewer's pane can show only the end of its reply, past job_id
// and the hashes (issue #86). The reply is in the conversation's log, whole,
// and is recorded from there.
func TestRunRecordsAReplyFromTheReviewersLogWhenThePaneShowsOnlyItsEnd(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	claude := t.TempDir()
	e.usage = AgentUsageSource{ClaudeProjects: claude}
	e.c = e.newController()
	att := e.toDeliveredReview()
	raw := string(encode(e.resultFor(att, workflow.ReviewStatusPassed)))
	text, _ := json.Marshal(raw)
	writeLog(t, filepath.Join(claude, "-repo", "c0ffee-2.jsonl"),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":`+string(text)+`}]}}`)

	e.reviewerObserved(att, runner.Observation{State: runner.ObsCompleted, ExternalID: "c0ffee-2", Session: "c0ffee-2"})
	e.tr.setPane("reviewer-session", raw[strings.Index(raw, `"status"`):]+"\n  Worked for 30s\n")
	e.tick()
	if got := e.reviewState(att); got != workflow.ReviewPassed {
		t.Fatalf("review state %q with the whole reply in the reviewer's log", got)
	}
}
