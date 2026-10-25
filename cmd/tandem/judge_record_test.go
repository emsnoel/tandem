package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/triage"
)

func readJudgeRecord(t *testing.T, root, id string) judgeRecord {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".tandem-log", "judge-"+id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec judgeRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestUpdateJudgeRecordSetsTheReviewerFact(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".tandem-log")
	_ = writeJudgeRecord(dir, "j4", "1", triage.Answers{}, triage.State{}, true, false, recordMeta{})
	yes := true
	if err := updateJudgeRecord(dir, "j4", func(r *judgeRecord) { r.ReviewChangedTree = &yes }); err != nil {
		t.Fatal(err)
	}
	if rec := readJudgeRecord(t, root, "j4"); rec.ReviewChangedTree == nil || !*rec.ReviewChangedTree {
		t.Fatalf("record %+v", rec)
	}
}

// WouldSkip is recorded even though nothing acts on it, so it can be compared
// with what a reviewer found.
func TestRecordCarriesWouldSkipAndTheVersionsItDependsOn(t *testing.T) {
	dir := t.TempDir()
	state := buildState("brief", "a diff", true, "ok")
	answers := triage.Answers{Implements: 0.97, ScopeExceeded: 0.01, Destructive: 0.0, Priority: 0.4}

	if err := writeJudgeRecord(dir, "id-1", "task_3", answers, state, true, true, recordMeta{}); err != nil {
		t.Fatalf("writeJudgeRecord: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "judge-id-1.json"))
	if err != nil {
		t.Fatalf("the record must exist: %v", err)
	}
	var rec struct {
		TaskID      string `json:"task_id"`
		Model       string `json:"model"`
		Contract    string `json:"contract_version"`
		Digest      string `json:"state_digest"`
		WouldSkip   bool   `json:"would_skip"`
		CheckPassed bool   `json:"check_passed"`
		Answers     struct {
			Implements float64 `json:"implements_task"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("the record must be valid JSON: %v", err)
	}
	if !rec.WouldSkip {
		t.Error("a clean batch with a passing check must record would_skip=true")
	}
	if rec.Model != decision.Model {
		t.Errorf("model: want %q, got %q", decision.Model, rec.Model)
	}
	if rec.Contract != decision.ContractVersion {
		t.Errorf("contract: want %q, got %q", decision.ContractVersion, rec.Contract)
	}
	if rec.Digest == "" {
		t.Error("the record must carry a state digest, or a retuned threshold silently reuses old comparisons")
	}
	if rec.TaskID != "task_3" {
		t.Errorf("task_id: want task_3, got %q", rec.TaskID)
	}
	if rec.Answers.Implements != 0.97 {
		t.Errorf("answers must be recorded raw, got %v", rec.Answers.Implements)
	}
	if !rec.CheckPassed {
		t.Error("check_passed must be recorded as given")
	}
}

// would_skip false has two causes, a failed check or an unsure model, so
// check_passed is recorded separately.
func TestRecordDistinguishesAFailedCheckFromALowConfidenceAnswer(t *testing.T) {
	dir := t.TempDir()
	state := buildState("brief", "a diff", true, "FAIL")
	confident := triage.Answers{Implements: 0.97, ScopeExceeded: 0.01, Destructive: 0.0}

	// Confident answer, failing check: would_skip false BECAUSE of the check.
	if err := writeJudgeRecord(dir, "id-2", "task_4", confident, state, false, false, recordMeta{}); err != nil {
		t.Fatalf("writeJudgeRecord: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "judge-id-2.json"))
	var rec struct {
		WouldSkip   bool `json:"would_skip"`
		CheckPassed bool `json:"check_passed"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("decoding the record: %v", err)
	}
	if rec.CheckPassed {
		t.Error("check_passed must be false when the check failed, whatever the answers say")
	}
	if rec.WouldSkip {
		t.Error("would_skip must be false when the check failed")
	}
}

// Different state must digest differently, or two judgments of different work
// would look like the same measurement when the labels are compared.
func TestRecordDigestsDistinguishDifferentState(t *testing.T) {
	a := buildState("brief", "diff one", true, "ok")
	b := buildState("brief", "diff two", true, "ok")
	if stateDigest(a) == stateDigest(b) {
		t.Fatal("two different diffs produced the same digest")
	}
	if stateDigest(a) != stateDigest(buildState("brief", "diff one", true, "ok")) {
		t.Fatal("the same state produced two different digests")
	}
}

func TestJudgeRecordCarriesTheEvidenceIdentityAndKeepsItsInput(t *testing.T) {
	root := triageRoot(t)
	logDir := filepath.Join(root, ".tandem-log")
	st := triage.State{TaskBrief: "make it greet", Diff: "diff --git a/x b/x\n+hello\n", CheckCommandExists: true}
	if err := writeJudgeRecord(logDir, "job-1", "1", triage.Answers{}, st, true, true, recordMeta{TreeHash: "tree-a"}); err != nil {
		t.Fatal(err)
	}
	rec := readJudgeRecord(t, root, "job-1")
	id := rec.Identity
	if id.Schema != triage.EvidenceSchema || id.PolicyHash != triage.PolicyHash() || id.InputHash == "" ||
		id.JudgeConfigHash != triage.JudgeConfigHash(decision.Model, decision.ContractVersion, decision.PolicyVersion) {
		t.Fatalf("%+v", id)
	}
	if id.ReviewerConfigHash != "" || id.Complete || rec.JobID != "job-1" || rec.JudgedTree != "tree-a" {
		t.Errorf("a judgment is not complete evidence until its review is in: %+v", rec)
	}
	// The exact input is kept, by its hash, and says what the judge saw.
	raw, err := os.ReadFile(filepath.Join(logDir, judgeInputName(id.InputHash)))
	if err != nil || !strings.Contains(string(raw), "make it greet") || !strings.Contains(string(raw), "+hello") || !verifyJudgeInput(logDir, id.InputHash) {
		t.Fatalf("%v %s", err, raw)
	}
	if fi, _ := os.Stat(filepath.Join(logDir, judgeInputName(id.InputHash))); fi.Mode().Perm() != 0o600 {
		t.Errorf("the kept input is mode %v", fi.Mode().Perm())
	}
	// A different input is kept under a different name; the same one under the same.
	other := persistJudgeInput(logDir, triage.State{TaskBrief: "another"})
	if other == id.InputHash || persistJudgeInput(logDir, st) != id.InputHash {
		t.Error("inputs are named by their content")
	}
	if verifyJudgeInput(logDir, strings.Repeat("0", 64)) {
		t.Error("an input that is not there verified")
	}

	// The review completes it: reviewer, tree, classification.
	yes := true
	if err := updateJudgeRecord(logDir, "job-1", func(r *judgeRecord) {
		r.Reviewed, r.ReviewChangedTree = &yes, &yes
		completeEvidence(r, currentReviewerConfig(root), "tree-a")
	}); err != nil {
		t.Fatal(err)
	}
	rec = readJudgeRecord(t, root, "job-1")
	if !rec.Identity.Complete || rec.Identity.ReviewerConfigHash == "" || rec.ReviewedTree != "tree-a" || rec.Classification != "false_negative" {
		t.Errorf("%+v", rec)
	}
	// Without the reviewer being nameable, or a tree, it stays incomplete.
	if err := updateJudgeRecord(logDir, "job-1", func(r *judgeRecord) { completeEvidence(r, "", "tree-a") }); err != nil {
		t.Fatal(err)
	}
	if rec := readJudgeRecord(t, root, "job-1"); rec.Identity.Complete {
		t.Error("an unnamed reviewer was treated as complete evidence")
	}
	if err := updateJudgeRecord(logDir, "job-1", func(r *judgeRecord) { completeEvidence(r, currentReviewerConfig(root), "") }); err != nil {
		t.Fatal(err)
	}
	if rec := readJudgeRecord(t, root, "job-1"); rec.Identity.Complete {
		t.Error("a review with no tree was treated as complete evidence")
	}
}

func TestAuditSelectionIsSettledBeforeTheVerdict(t *testing.T) {
	root := triageRoot(t)
	logDir := filepath.Join(root, ".tandem-log")
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("job-%d", i)
		confident := triage.Answers{Implements: 1, EvidenceSufficient: 1}
		if err := writeJudgeRecord(logDir, id, id, confident, triage.State{TaskBrief: id}, true, true, recordMeta{}); err != nil {
			t.Fatal(err)
		}
		if err := writeJudgeRecord(logDir, id+"x", id+"x", triage.Answers{}, triage.State{TaskBrief: id}, true, false, recordMeta{}); err != nil {
			t.Fatal(err)
		}
		a, b := readJudgeRecord(t, root, id), readJudgeRecord(t, root, id+"x")
		if a.AuditSelected != triage.AuditSelected(id, triage.PolicyHash()) || b.AuditSelected != triage.AuditSelected(id+"x", triage.PolicyHash()) {
			t.Fatalf("%s: audit selection does not follow the job and the policy", id)
		}
	}
	// Whatever the verdict, a job is chosen or not by its id and the policy: the
	// same id written as a skip and as a review gives one answer.
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("same-%d", i)
		writeJudgeRecord(logDir, id, id, triage.Answers{}, triage.State{}, true, true, recordMeta{})
		first := readJudgeRecord(t, root, id).AuditSelected
		writeJudgeRecord(logDir, id, id, triage.Answers{}, triage.State{}, false, false, recordMeta{})
		if readJudgeRecord(t, root, id).AuditSelected != first {
			t.Errorf("%s: the audit choice changed with the verdict", id)
		}
	}
}

func TestTreeDigestNamesATreeByItsContent(t *testing.T) {
	a := treeDigest(map[string]string{"a.go": "h1", "b.go": "h2"})
	if a == "" || a != treeDigest(map[string]string{"b.go": "h2", "a.go": "h1"}) {
		t.Error("the digest depends on map order")
	}
	for name, other := range map[string]map[string]string{
		"a changed file": {"a.go": "h1", "b.go": "h3"},
		"a renamed file": {"a.go": "h1", "c.go": "h2"},
		"an added file":  {"a.go": "h1", "b.go": "h2", "c.go": "h3"},
		"a swap":         {"a.go": "h2", "b.go": "h1"},
	} {
		if treeDigest(other) == a {
			t.Errorf("%s did not change the digest", name)
		}
	}
	if treeDigest(nil) != "" {
		t.Error("an unknown tree must read as unknown, not as a digest")
	}
}
