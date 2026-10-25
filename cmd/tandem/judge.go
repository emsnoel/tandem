package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/decision/jev"
	"github.com/emsnoel/tandem/internal/evidence"
	"github.com/emsnoel/tandem/internal/triage"
)

// judgeTimeout bounds the call. A judgment slower than the review it might
// replace costs more than it saves.
const judgeTimeout = 10 * time.Second

// noKeyReason explains an unavailable judgment in the durable record.
// A missing key usually means the user has no active Jev subscription.
const noKeyReason = "TYPESAFE_API_KEY is not set, which usually means no active Jev subscription"

// jevClient is the Jev call judgeTask and `decision assess` make; an interface
// so tests can see whether a request was made.
type jevClient interface {
	Ask(ctx context.Context, model string, state any,
		questions map[string]decision.Question) (map[string]decision.Answer, error)
}

var newJevClient = func(key string, timeout time.Duration) jevClient {
	return jev.New(key, timeout)
}

// judgeRequest carries the task diff and check result measured by the build
// loop, plus the identifiers used by the durable judgment record.
type judgeRequest struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	TaskBrief string `json:"task_brief"`
	Diff      string `json:"diff"`
	// CheckCommandExists is explicit because a passing check can print nothing.
	CheckCommandExists bool   `json:"check_command_exists"`
	CheckPassed        bool   `json:"check_passed"`
	CheckOutput        string `json:"check_output"`
	// TreeHash is the digest of the working tree the judgment is of, so the
	// review it is later compared with can be shown to be of the same tree.
	TreeHash string `json:"tree_hash,omitempty"`
}

type Verdict struct {
	Answers   triage.Answers
	State     triage.State
	WouldSkip bool
	RecordID  string
}

// buildState assembles what the model is shown. The diff is redacted, since
// this is the last point before it leaves the machine, and then bounded, with
// the bound recorded so the answer knows it saw a partial diff.
func buildState(brief, diff string, checkExists bool, checkOutput string) triage.State {
	diff = evidence.Redact(diff)
	truncated := false
	if len(diff) > triage.MaxDiffBytes {
		diff = diff[:triage.MaxDiffBytes]
		truncated = true
	}
	return triage.State{
		TaskBrief:          evidence.Redact(brief),
		Diff:               diff,
		CheckCommandExists: checkExists,
		CheckOutput:        evidence.Redact(checkOutput),
		Truncated:          truncated,
	}
}

func judgeTask(ctx context.Context, c jevClient, req judgeRequest, root string) (Verdict, error) {
	state := buildState(req.TaskBrief, req.Diff, req.CheckCommandExists, req.CheckOutput)
	recordUnavailable := func(reason string) (Verdict, error) {
		logDir := filepath.Join(root, ".tandem-log")
		_ = writeJudgeRecord(logDir, req.ID, req.TaskID, triage.Answers{}, state, req.CheckPassed, false, recordMeta{TreeHash: req.TreeHash})
		_ = updateJudgeRecord(logDir, req.ID, func(rec *judgeRecord) {
			rec.Verdict = "unavailable"
			rec.Unavailable = reason
		})
		return Verdict{}, errors.New(reason)
	}

	if apiKey() == "" {
		return recordUnavailable(noKeyReason)
	}
	answers, err := c.Ask(ctx, decision.Model, state, triage.Questions())
	if errors.Is(err, jev.ErrUnauthorized) {
		// Its own message names the subscription.
		return recordUnavailable(err.Error())
	}
	if err != nil {
		return recordUnavailable(fmt.Sprintf("the provider call failed (%v)", err))
	}
	parsed, err := triage.Parse(answers)
	if err != nil {
		return recordUnavailable(fmt.Sprintf("the response could not be read (%v)", err))
	}

	// WouldSkip records that the answers clear the thresholds on a passing check
	// and an untruncated diff. It is not permission to skip: that also needs a
	// diff known to be the attempt's complete change (triage.SkipFacts).
	wouldSkip := req.CheckPassed && !state.Truncated && parsed.Confident()
	// Best effort: a failure to record must never fail a delivered verdict.
	_ = writeJudgeRecord(filepath.Join(root, ".tandem-log"), req.ID, req.TaskID, parsed, state, req.CheckPassed, wouldSkip, recordMeta{TreeHash: req.TreeHash})
	return Verdict{Answers: parsed, State: state, WouldSkip: wouldSkip, RecordID: req.ID}, nil
}
