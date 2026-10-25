package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

func TestLastReviewObjectFindsTheReplyAmongNoise(t *testing.T) {
	const job = "49f1d12fa5479d76940d891b9c6917c4"
	other := `{"job_id":"00000000000000000000000000000000","status":"passed"}`
	mine := `{"job_id":"` + job + `","status":"passed","findings":[]}`
	text := "> prompt {example}\n• " + other + "\n• " + mine + "\n  Worked for 2s\n"
	if got := string(lastReviewObject(text, job)); got != mine {
		t.Fatalf("got %q, want %q", got, mine)
	}
	if got := lastReviewObject(text, "ffffffffffffffffffffffffffffffff"); got != nil {
		t.Fatalf("an object for another job was returned: %q", got)
	}
	if got := lastReviewObject("{ not json "+job, job); got != nil {
		t.Fatalf("broken JSON was returned: %q", got)
	}
}

func TestLastReviewObjectJoinsALineTheCLIWrapped(t *testing.T) {
	const job = "49f1d12fa5479d76940d891b9c6917c4"
	hash := "d781c704c8d88a05719572136f455c769b360a0106d63cf6a00657202008a0d7"
	text := "• {\"schema_version\":1,\"job_id\":\"" + job + "\",\"tree_hash\":\"" + hash[:40] + "\n  " + hash[40:] + "\",\"status\":\"passed\",\"findings\":[]}\n  Worked for 2s\n"
	want := `{"schema_version":1,"job_id":"` + job + `","tree_hash":"` + hash + `","status":"passed","findings":[]}`
	if got := string(lastReviewObject(text, job)); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// echoedPane is a reviewer pane as Codex shows it: the review prompt, which
// carries the job's real ids in its example, then each reply in turn.
func echoedPane(job string, replies ...string) string {
	prompt := reviewPrompt(reviewPromptData{Task: 1, Slug: "task-1", Job: job, Attempt: strings.Repeat("b", 32), PatchHash: strings.Repeat("c", 64), Tree: strings.Repeat("d", 64), Patch: "diff"})
	text := "› " + prompt + "\n"
	for _, r := range replies {
		text += "\n• " + r + "\n"
	}
	return text
}

// wrappedReply is a changes_requested reply whose tree hash and finding
// message the CLI wrapped and indented, and the object it stands for.
func wrappedReply(job string) (shown, want string) {
	head := `{"schema_version":1,"job_id":"` + job + `","attempt_id":"` + strings.Repeat("b", 32) + `","patch_hash":"` + strings.Repeat("c", 64) + `",`
	tree := strings.Repeat("d", 64)
	shown = head + "\n  " + `"tree_hash":"` + tree[:30] + "\n  " + tree[30:] + `","status":"changes_requested","findings":[{"severity":"medium","path":"a.go","disposition":"open","message":"the loop` + "\n  " + `never ends","start_line":1,"end_line":2}]}`
	want = head + `"tree_hash":"` + tree + `","status":"changes_requested","findings":[{"severity":"medium","path":"a.go","disposition":"open","message":"the loopnever ends","start_line":1,"end_line":2}]}`
	return shown, want
}

func TestLastReviewObjectSkipsTheEchoedPromptForAWrappedReply(t *testing.T) {
	job := strings.Repeat("a", 32)
	shown, want := wrappedReply(job)
	if got := string(lastReviewObject(echoedPane(job, shown), job)); got != want {
		t.Fatalf("got %q, want the wrapped reply %q", got, want)
	}
}

func TestLastReviewObjectSkipsTheEchoedPromptForAnUnwrappedReply(t *testing.T) {
	job := strings.Repeat("a", 32)
	reply := `{"schema_version":1,"job_id":"` + job + `","status":"passed","findings":[]}`
	if got := string(lastReviewObject(echoedPane(job, reply), job)); got != reply {
		t.Fatalf("got %q, want %q", got, reply)
	}
}

func TestLastReviewObjectIgnoresAnEchoedPromptWithoutAReply(t *testing.T) {
	job := strings.Repeat("a", 32)
	if got := lastReviewObject(echoedPane(job), job); got != nil {
		t.Fatalf("the prompt's own example was returned: %q", got)
	}
}

func TestLastReviewObjectTakesTheResendOverARejectedReply(t *testing.T) {
	job := strings.Repeat("a", 32)
	rejected := `{"schema_version":1,"job_id":"` + job + `","attempt_id":"` + strings.Repeat("b", 33) + `","status":"passed","findings":[]}`
	shown, want := wrappedReply(job)
	if got := string(lastReviewObject(echoedPane(job, rejected, shown), job)); got != want {
		t.Fatalf("got %q, want the resend %q", got, want)
	}
}

func TestRunRecordsAWrappedPaneReviewAfterTheEchoedPrompt(t *testing.T) {
	e := newCEnv(t)
	att := e.toDeliveredReview()
	result := e.resultFor(att, workflow.ReviewStatusPassed)
	prompt := reviewPrompt(reviewPromptData{Task: 1, Slug: "task-1", Job: result.JobID, Attempt: result.AttemptID, PatchHash: result.PatchHash, Tree: result.TreeHash, Patch: "diff"})
	raw := string(encode(result))
	cut := strings.Index(raw, result.TreeHash) + 30
	pane := "› " + prompt + "\n\n• " + raw[:cut] + "\n  " + raw[cut:] + "\n  Worked for 2s\n"

	e.reviewerFinished(att, "reviewer-session")
	e.tr.setPane("reviewer-session", pane)
	e.tick()
	if rejects, _ := e.st.RunReviewRejections(ctx, e.runID); len(rejects) != 0 {
		t.Fatalf("a valid wrapped reply was rejected: %+v", rejects)
	}
	if got := e.reviewState(att); got != workflow.ReviewPassed {
		t.Fatalf("review state %q with a wrapped reply after the echoed prompt", got)
	}
}

func TestRunRecordsAPaneReviewItReadsFromTheReviewersPane(t *testing.T) {
	e := newCEnv(t)
	att := e.toDeliveredReview()
	raw, _ := json.MarshalIndent(e.resultFor(att, workflow.ReviewStatusPassed), "", "  ")
	pane := "• " + string(raw) + "\n  Worked for 2s\n"

	e.reviewerObserved(att, runner.Observation{State: runner.ObsRunning})
	e.tr.setPane("reviewer-session", pane)
	e.tick()
	if e.reviewState(att) == workflow.ReviewPassed {
		t.Fatal("a reply was taken while the reviewer was still working")
	}

	e.reviewerFinished(att, "reviewer-session")
	e.tick()
	if got := e.reviewState(att); got != workflow.ReviewPassed {
		t.Fatalf("review state %q after the reviewer settled with a reply in its pane", got)
	}
}

func TestRunLeavesAPaneWithoutAReplyAlone(t *testing.T) {
	e := newCEnv(t)
	att := e.toDeliveredReview()
	e.reviewerFinished(att, "reviewer-session")
	e.tr.setPane("builder-session", "thinking about it\n")
	e.tick()
	if got := e.reviewState(att); got == workflow.ReviewPassed {
		t.Fatalf("review state %q with no reply in the pane", got)
	}
	if run := e.runRow(); run.State == "paused" {
		t.Fatalf("a missing reply paused the run (%s)", run.PauseCode)
	}
}

// toDeliveredReview takes task-1 to a delivered review prompt.
func (e *cenv) toDeliveredReview() workflow.Attempt {
	e.t.Helper()
	e.start()
	att := e.toReview("task-1", 1)
	for i := 0; i < 8 && e.delivered()[effectReviewPrompt] == 0; i++ {
		e.tick()
	}
	if e.delivered()[effectReviewPrompt] != 1 {
		e.t.Fatalf("the review prompt was not delivered: %v", e.delivered())
	}
	return att
}
