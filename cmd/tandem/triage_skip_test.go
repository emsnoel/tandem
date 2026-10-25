package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/triage"
)

// confidentVerdict is a judgment that clears every skip threshold.
func confidentVerdict(id string) Verdict {
	return Verdict{
		RecordID: id, WouldSkip: true,
		Answers: triage.Answers{Implements: 0.97, ScopeExceeded: 0.01, Priority: 0.1, EvidenceSufficient: 0.95},
	}
}

func verdictFor(id string, wouldSkip bool) Verdict {
	if wouldSkip {
		return confidentVerdict(id)
	}
	return Verdict{RecordID: id}
}

// A legacy diff is HEAD-relative in a shared worktree, so it cannot prove it is
// this attempt's complete change. Turning the switch on must not let it skip.
func TestLegacySkipRemainsShadowOnly(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	root, dir := buildRepo(t, "check", 0)
	judgeOn(t, dir)
	seedPairs(t, root, 30, 10)
	f := &fakeAdvance{statuses: []string{"idle"}, incompleteDiff: true, judgeVerdict: confidentVerdict("r1")}
	if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
		t.Fatalf("printed %q; an unproven diff must still get a review", got)
	}
	if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing {
		t.Fatalf("task %+v", task)
	}
	if rec := readJudgeRecord(t, root, "r1"); !rec.WouldSkip {
		t.Fatal("the shadow judgment was not recorded")
	}
}

func TestLiveAdvanceNeverClaimsACompleteDiff(t *testing.T) {
	if liveAdvanceDeps.diffComplete {
		t.Fatal("the legacy diff must not be reported as complete")
	}
}

func TestSkipRechecksTheFactsAtTheFinalBranch(t *testing.T) {
	t.Setenv("HERDR_TRIAGE_SKIP", "1")
	for name, v := range map[string]Verdict{
		"flag without answers": {RecordID: "r1", WouldSkip: true},
		"truncated diff": func() Verdict {
			v := confidentVerdict("r1")
			v.State.Truncated = true
			return v
		}(),
		"weak evidence": func() Verdict {
			v := confidentVerdict("r1")
			v.Answers.EvidenceSufficient = 0.5
			return v
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := buildRepo(t, "check", 0)
			judgeOn(t, dir)
			seedPairs(t, root, 30, 10)
			f := &fakeAdvance{statuses: []string{"idle"}, judgeVerdict: v}
			if got := advance(t, f, root, reviewArgs("e1", "")...); !strings.HasPrefix(got, "review 1 ") {
				t.Fatalf("printed %q", got)
			}
			if task := load(t, dir).Tasks[0]; task.Status != build.StatusReviewing {
				t.Fatalf("task %+v", task)
			}
		})
	}
}

func TestJudgeTaskWouldNotSkipOnIncompleteOrWeakEvidence(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-test")
	long := judgeRequest{ID: "t1", CheckPassed: true, Diff: strings.Repeat("+x\n", triage.MaxDiffBytes)}
	v, err := judgeTask(context.Background(), &fakeJev{answers: confidentAnswers()}, long, t.TempDir())
	if err != nil || v.WouldSkip || !v.State.Truncated {
		t.Errorf("truncated diff: v=%+v err=%v", v, err)
	}
	weak := confidentAnswers()
	weak[triage.QEvidence] = decision.Answer{Type: "noul", Noul: 0.5}
	v, err = judgeTask(context.Background(), &fakeJev{answers: weak}, judgeRequest{ID: "t2", CheckPassed: true}, t.TempDir())
	if err != nil || v.WouldSkip {
		t.Errorf("weak evidence: v=%+v err=%v", v, err)
	}
	v, err = judgeTask(context.Background(), &fakeJev{answers: confidentAnswers()}, judgeRequest{ID: "t3", CheckPassed: true}, t.TempDir())
	if err != nil || !v.WouldSkip {
		t.Errorf("a complete confident judgment must still record would_skip: v=%+v err=%v", v, err)
	}
}

func TestJudgeRecordNamesTheSkipRule(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "ts-test")
	root := t.TempDir()
	if _, err := judgeTask(context.Background(), &fakeJev{answers: confidentAnswers()}, judgeRequest{ID: "s1", CheckPassed: true}, root); err != nil {
		t.Fatal(err)
	}
	if rec := readJudgeRecord(t, root, "s1"); rec.SkipRule != triage.SkipRule {
		t.Fatalf("skip rule %q, want %q", rec.SkipRule, triage.SkipRule)
	}
}

// Rows scored under an older, looser skip rule say nothing about the current
// one, so they are excluded rather than counted.
func TestRowsFromAnotherSkipRuleAreNotPooled(t *testing.T) {
	root := t.TempDir()
	seedPairs(t, root, 30, 10)
	logDir := filepath.Join(root, ".tandem-log")
	summarize := func() triage.Report {
		rows, corrupt, err := loadRows(logDir)
		if err != nil {
			t.Fatal(err)
		}
		return triage.Summarize(rows, triage.CurrentCohort(currentReviewerConfig(root)), corrupt)
	}
	if got := summarize(); got.Pairs != 40 || got.Excluded != 0 {
		t.Fatalf("current rows: %+v", got)
	}
	if err := updateJudgeRecord(logDir, "seed-0", func(rec *judgeRecord) { rec.Identity = triage.EvidenceIdentity{} }); err != nil {
		t.Fatal(err)
	}
	if got := summarize(); got.Pairs != 39 || got.Excluded != 1 {
		t.Fatalf("after one legacy row: %+v", got)
	}
}
