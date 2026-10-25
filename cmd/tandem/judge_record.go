package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/triage"
)

// stateDigest identifies the exact question that was asked. It covers the
// contract, policy and model versions as well as the state, because a
// threshold change makes the same diff a different question.
func stateDigest(s triage.State) string {
	h := sha256.New()
	for _, part := range []string{
		s.TaskBrief, s.Diff, s.CheckOutput,
		decision.ContractVersion, decision.PolicyVersion, decision.Model,
	} {
		_, _ = h.Write([]byte(part))
		// A separator, so ("ab","c") and ("a","bc") cannot collide.
		_, _ = h.Write([]byte{0})
	}
	if s.CheckCommandExists {
		_, _ = h.Write([]byte("check"))
	}
	if s.Truncated {
		_, _ = h.Write([]byte("truncated"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// judgeRecord is one row of the calibration set. WouldSkip is recorded even
// when nothing acts on it, so it can be compared with what a reviewer found.
//
// The row carries only digests and labels. The exact input is kept beside it,
// by hash and with mode 0600 (see persistJudgeInput).
type judgeRecord struct {
	ID                string             `json:"id"`
	TaskID            string             `json:"task_id"`
	Timestamp         string             `json:"ts"`
	Model             string             `json:"model"`
	ContractVersion   string             `json:"contract_version"`
	PolicyVersion     string             `json:"policy_version"`
	StateDigest       string             `json:"state_digest"`
	Truncated         bool               `json:"truncated"`
	CheckPassed       bool               `json:"check_passed"`
	Answers           map[string]float64 `json:"answers"`
	WouldSkip         bool               `json:"would_skip"`
	Verdict           string             `json:"verdict"`
	ReviewChangedTree *bool              `json:"review_changed_tree"`
	Reviewed          *bool              `json:"reviewed"`
	Unavailable       string             `json:"unavailable,omitempty"`
	// SkipRule is the triage.SkipRule WouldSkip was scored under. Absent on
	// rows written before the rule carried a name.
	SkipRule string `json:"skip_rule,omitempty"`

	// JobID is the job the judgment and its review both belong to.
	JobID string `json:"job_id,omitempty"`
	// Identity names exactly what this row is evidence about; rows without it
	// were written before it existed and never count toward the current cohort.
	Identity triage.EvidenceIdentity `json:"identity"`
	// JudgedTree and ReviewedTree are the digests of the tree the judgment was
	// of and the tree the reviewer started from. A pair needs them equal.
	JudgedTree   string `json:"judged_tree,omitempty"`
	ReviewedTree string `json:"reviewed_tree,omitempty"`
	// AuditSelected was decided from the job and the policy before the verdict.
	AuditSelected bool `json:"audit_selected"`
	// Classification is the outcome once the review is in: true_positive,
	// false_negative (a miss), false_positive or true_negative.
	Classification string `json:"classification,omitempty"`
}

// recordMeta is what a row needs beyond the judgment itself.
type recordMeta struct {
	// TreeHash is the digest of the working tree the judgment was made on.
	TreeHash string
}

// treeDigest names a tree by its files and their content hashes, in order.
func treeDigest(files map[string]string) string {
	if len(files) == 0 {
		return ""
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(files[p]))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// judgeInputName is where the exact input of a judgment is kept, named by its
// hash so that a row's reference proves the bytes are what was judged.
func judgeInputName(hash string) string { return "judge-input-" + hash + ".json" }

// persistJudgeInput keeps the exact input the judge was shown, and the versions
// it was shown under, so a judgment can be audited later and not only counted.
// It is the same text that was sent to the provider, written 0600 beside the
// calibration rows. It returns the hash that names it, or "" if it could not be
// kept, which leaves the row's evidence incomplete and so out of the cohort.
func persistJudgeInput(logDir string, s triage.State) string {
	body, err := json.Marshal(struct {
		State    triage.State `json:"state"`
		Model    string       `json:"model"`
		Contract string       `json:"contract_version"`
		Policy   string       `json:"policy_version"`
	}{s, decision.Model, decision.ContractVersion, decision.PolicyVersion})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	path := filepath.Join(logDir, judgeInputName(hash))
	if have, err := os.ReadFile(path); err == nil && string(have) == string(body) {
		return hash
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return ""
	}
	if err := os.Rename(tmp, path); err != nil {
		return ""
	}
	return hash
}

// verifyJudgeInput reports whether the input a row names is there and is what
// its hash says.
func verifyJudgeInput(logDir, hash string) bool {
	body, err := os.ReadFile(filepath.Join(logDir, judgeInputName(hash)))
	if err != nil {
		return false
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) == hash
}

// currentReviewerConfig identifies the reviewer a judgment is compared with:
// the reviewer pane's kind and launch model from reviewer.json, and the review
// prompt. "" when no reviewer is recorded. A reviewer on its CLI's default
// model is named "default".
func currentReviewerConfig(root string) string {
	rec, err := config.LoadReviewer(paths.Relay(root))
	if err != nil {
		return ""
	}
	model := rec.Model
	if model == "" {
		model = "default"
	}
	return triage.ReviewerConfigHash(rec.Kind, model, build.ReviewPromptID())
}

// completeEvidence attaches what the review adds to a row: the reviewer's
// configuration, the tree it started from and, from the measured outcome, the
// classification. The identity is complete only if every part is there.
func completeEvidence(rec *judgeRecord, reviewerConfig, reviewedTree string) {
	rec.Identity.ReviewerConfigHash = reviewerConfig
	rec.ReviewedTree = reviewedTree
	rec.Identity.Complete = rec.Identity.Finished() && rec.JudgedTree != "" && reviewedTree != ""
	if rec.Reviewed == nil || !*rec.Reviewed || rec.ReviewChangedTree == nil {
		return
	}
	switch changed := *rec.ReviewChangedTree; {
	case rec.WouldSkip && changed:
		rec.Classification = "false_negative"
	case rec.WouldSkip:
		rec.Classification = "true_negative"
	case changed:
		rec.Classification = "true_positive"
	default:
		rec.Classification = "false_positive"
	}
}

// writeJudgeRecord appends one row to the calibration set. Best effort: callers
// ignore the error, so a full disk never fails a delivered verdict.
func writeJudgeRecord(logDir, id, taskID string, a triage.Answers, st triage.State, checkPassed, wouldSkip bool, meta recordMeta) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	rec := judgeRecord{
		ID:              id,
		TaskID:          taskID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Model:           decision.Model,
		ContractVersion: decision.ContractVersion,
		PolicyVersion:   decision.PolicyVersion,
		StateDigest:     stateDigest(st),
		Truncated:       st.Truncated,
		// Recorded separately from WouldSkip: a red suite and a hesitant answer are
		// different findings.
		CheckPassed: checkPassed,
		Answers: map[string]float64{
			triage.QImplements:  a.Implements,
			triage.QScope:       a.ScopeExceeded,
			triage.QDestructive: a.Destructive,
			triage.QPriority:    a.Priority,
			triage.QEvidence:    a.EvidenceSufficient,
		},
		WouldSkip: wouldSkip,
		SkipRule:  triage.SkipRule,
		JobID:     id,
		Identity: triage.EvidenceIdentity{Schema: triage.EvidenceSchema, PolicyHash: triage.PolicyHash(),
			JudgeConfigHash: triage.JudgeConfigHash(decision.Model, decision.ContractVersion, decision.PolicyVersion), InputHash: persistJudgeInput(logDir, st)},
		JudgedTree: meta.TreeHash,
		// Decided from the job and the policy alone, before there is a verdict
		// to influence it.
		AuditSelected: triage.AuditSelected(id, triage.PolicyHash()),
	}
	if wouldSkip {
		rec.Verdict = "clean"
	} else {
		rec.Verdict = "review"
	}
	return saveJudgeRecord(logDir, rec)
}

func saveJudgeRecord(logDir string, rec judgeRecord) error {
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	// Written to a temp name and moved into place, so a reader never sees a
	// half-written row.
	tmp := filepath.Join(logDir, "judge-"+rec.ID+".json.tmp")
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(logDir, "judge-"+rec.ID+".json"))
}

// updateJudgeRecord atomically adds facts learned after the initial verdict.
func updateJudgeRecord(logDir, id string, mut func(*judgeRecord)) error {
	path := filepath.Join(logDir, "judge-"+id+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rec judgeRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return err
	}
	mut(&rec)
	return saveJudgeRecord(logDir, rec)
}
