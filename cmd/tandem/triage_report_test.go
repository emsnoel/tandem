package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/triage"
)

// triageRoot is a checkout with a recorded reviewer pane, so a reviewer
// configuration can be named.
func triageRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	relay := filepath.Join(root, ".tandem-relay")
	if err := os.MkdirAll(relay, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relay, "reviewer.json"), []byte(`{"kind":"codex","name":"reviewer","model":"gpt-x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// currentTriageReportFixture is a complete, matched observation under the
// current cohort, with its exact input kept.
func currentTriageReportFixture(root, id string) judgeRecord {
	logDir := filepath.Join(root, ".tandem-log")
	os.MkdirAll(logDir, 0o755)
	yes, no := true, false
	return judgeRecord{ID: id, JobID: id, Verdict: "clean", WouldSkip: true, Reviewed: &yes, ReviewChangedTree: &no,
		ContractVersion: decision.ContractVersion, PolicyVersion: decision.PolicyVersion, Model: decision.Model, SkipRule: triage.SkipRule,
		JudgedTree: "tree-" + id, ReviewedTree: "tree-" + id,
		Identity: triage.EvidenceIdentity{Schema: triage.EvidenceSchema, PolicyHash: triage.PolicyHash(),
			JudgeConfigHash:    triage.JudgeConfigHash(decision.Model, decision.ContractVersion, decision.PolicyVersion),
			ReviewerConfigHash: currentReviewerConfig(root), InputHash: persistJudgeInput(logDir, triage.State{TaskBrief: id}), Complete: true}}
}

func writeTriageReportFixture(t *testing.T, root, id string, rec judgeRecord) {
	t.Helper()
	dir := filepath.Join(root, ".tandem-log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "judge-"+id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func reportJSON(t *testing.T, root string) map[string]any {
	t.Helper()
	var got map[string]any
	out := captureStdout(t, func() int { return cmdTriageReport([]string{"--root", root, "--json"}) })
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, out)
	}
	return got
}

func seedCohort(t *testing.T, root string, skipPairs, changed int) {
	t.Helper()
	yes := true
	for i := 0; i < skipPairs+changed; i++ {
		id := fmt.Sprintf("p%d", i)
		rec := currentTriageReportFixture(root, id)
		if i >= skipPairs {
			rec.WouldSkip, rec.Verdict, rec.ReviewChangedTree = false, "review", &yes
		}
		writeTriageReportFixture(t, root, id, rec)
	}
}

func TestTriageReportCountsExcludesAndExplains(t *testing.T) {
	root := triageRoot(t)
	writeTriageReportFixture(t, root, "pair", currentTriageReportFixture(root, "pair"))
	unfinished := currentTriageReportFixture(root, "unfinished")
	unfinished.ReviewChangedTree = nil
	writeTriageReportFixture(t, root, "unfinished", unfinished)
	legacy := currentTriageReportFixture(root, "legacy")
	legacy.Identity = triage.EvidenceIdentity{}
	writeTriageReportFixture(t, root, "legacy", legacy)
	// Atomic-write temporary files are not rows.
	os.WriteFile(filepath.Join(root, ".tandem-log", "judge-pending.json.tmp"), []byte("{"), 0o600)

	out := captureStdout(t, func() int { return run([]string{"triage", "report", "--root", root}) })
	for _, want := range []string{
		"pairs: 1   would-skip pairs: 1   reviewer changed the tree: 0   misses: 0   excluded: 2",
		"health: healthy   missing pairs: 1",
		"excluded legacy_record: 1",
		"excluded awaiting_review: 1",
		"gate: closed (needs 30 would-skip pairs, 10 pairs with a change, 0 misses)",
		"Zero misses in 30 still allows a true miss rate near 10%.",
		"One miss under the current contract, policy and model keeps the gate closed until one of them changes.",
		"not a proof of safety",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestTriageReportJSONAndOpenGate(t *testing.T) {
	root := triageRoot(t)
	seedCohort(t, root, 30, 10)
	got := reportJSON(t, root)
	for key, want := range map[string]any{"pairs": float64(40), "skip_pairs": float64(30), "reviewer_changed": float64(10), "misses": float64(0), "excluded": float64(0),
		"gate_open": true, "healthy": true, "corrupt": float64(0), "missing_pairs": float64(0), "closed": false} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if _, ok := got["excluded_reasons"].(map[string]any); !ok {
		t.Errorf("excluded_reasons must be an object: %v", got["excluded_reasons"])
	}
	if up, _ := got["miss_rate_upper_95"].(float64); up <= 0 || up >= 0.2 {
		t.Errorf("an upper bound on the miss rate: %v", got["miss_rate_upper_95"])
	}
	out := captureStdout(t, func() int { return cmdTriageReport([]string{"--root", root}) })
	if !strings.Contains(out, "gate: open\n") || !strings.Contains(out, "health: healthy") {
		t.Fatalf("gate not open: %s", out)
	}
}

func TestCalibrationCorruptRowClosesGate(t *testing.T) {
	for name, corrupt := range map[string]func(t *testing.T, root string){
		"not json": func(t *testing.T, root string) {
			os.WriteFile(filepath.Join(root, ".tandem-log", "judge-bad.json"), []byte("{"), 0o600)
		},
		"a directory": func(t *testing.T, root string) { os.Mkdir(filepath.Join(root, ".tandem-log", "judge-dir.json"), 0o755) },
		"wrong id": func(t *testing.T, root string) {
			writeTriageReportFixture(t, root, "named-one-thing", currentTriageReportFixture(root, "but-says-another"))
		},
		"empty id": func(t *testing.T, root string) {
			r := currentTriageReportFixture(root, "x")
			r.ID = ""
			writeTriageReportFixture(t, root, "x", r)
		},
		"input missing": func(t *testing.T, root string) {
			r := currentTriageReportFixture(root, "lost")
			os.Remove(filepath.Join(root, ".tandem-log", judgeInputName(r.Identity.InputHash)))
			writeTriageReportFixture(t, root, "lost", r)
		},
		"input altered": func(t *testing.T, root string) {
			r := currentTriageReportFixture(root, "altered")
			os.WriteFile(filepath.Join(root, ".tandem-log", judgeInputName(r.Identity.InputHash)), []byte(`{"state":"edited after the fact"}`), 0o600)
			writeTriageReportFixture(t, root, "altered", r)
		},
	} {
		root := triageRoot(t)
		seedCohort(t, root, 30, 10) // an otherwise open gate
		if got := reportJSON(t, root); got["gate_open"] != true {
			t.Fatalf("%s: fixture: %v", name, got)
		}
		corrupt(t, root)
		got := reportJSON(t, root)
		if got["gate_open"] != false || got["healthy"] != false || got["corrupt"].(float64) < 1 {
			t.Errorf("%s: a corrupt row must close the gate: %v", name, got)
		}
		// The sample is as it was: health is reported apart from the counts.
		if got["pairs"] != float64(40) || got["skip_pairs"] != float64(30) {
			t.Errorf("%s: %v", name, got)
		}
		out := captureStdout(t, func() int { return cmdTriageReport([]string{"--root", root}) })
		if !strings.Contains(out, "UNHEALTHY") || !strings.Contains(out, "corrupt or unreadable") {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}

func TestCalibrationConfigChangeSeparatesCohorts(t *testing.T) {
	root := triageRoot(t)
	seedCohort(t, root, 30, 10)
	if got := reportJSON(t, root); got["gate_open"] != true {
		t.Fatal(got)
	}
	// The builder never reviews, so a different builder is the same cohort.
	if err := os.WriteFile(filepath.Join(root, ".tandem-relay", "builder.json"), []byte(`{"kind":"claude","autonomy":"auto"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := reportJSON(t, root); got["gate_open"] != true || got["pairs"] != float64(40) {
		t.Fatalf("a builder change moved the reviewer's cohort: %v", got)
	}
	// A different reviewer kind, or model, is a different reviewer: the evidence
	// gathered with the old one is shown as excluded, not pooled, and the gate
	// closes.
	for _, rec := range []string{`{"kind":"claude","name":"reviewer","model":"gpt-x"}`, `{"kind":"codex","name":"reviewer","model":"gpt-y"}`} {
		if err := os.WriteFile(filepath.Join(root, ".tandem-relay", "reviewer.json"), []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
		got := reportJSON(t, root)
		reasons := got["excluded_reasons"].(map[string]any)
		if got["pairs"] != float64(0) || got["gate_open"] != false || reasons["other_cohort"] != float64(40) {
			t.Errorf("%s: %v", rec, got)
		}
	}
	// With no reviewer recorded the reviewer cannot be named, and nothing counts.
	os.Remove(filepath.Join(root, ".tandem-relay", "reviewer.json"))
	if got := reportJSON(t, root); got["pairs"] != float64(0) || got["gate_open"] != false {
		t.Errorf("%v", got)
	}
}

func TestTriageReportExcludesWhatItCannotPair(t *testing.T) {
	root := triageRoot(t)
	cases := map[string]func(*judgeRecord){
		"judge_unavailable":   func(r *judgeRecord) { r.Verdict = "unavailable" },
		"incomplete_identity": func(r *judgeRecord) { r.Identity.Complete = false },
		"tree_unknown":        func(r *judgeRecord) { r.JudgedTree = "" },
		"tree_mismatch":       func(r *judgeRecord) { r.ReviewedTree = "another-tree" },
		"truncated_input":     func(r *judgeRecord) { r.Truncated = true },
		"not_reviewed":        func(r *judgeRecord) { no := false; r.Reviewed = &no },
		"other_cohort":        func(r *judgeRecord) { r.Identity.JudgeConfigHash = "older" },
		"legacy_record":       func(r *judgeRecord) { r.Identity = triage.EvidenceIdentity{} },
	}
	for reason, mut := range cases {
		rec := currentTriageReportFixture(root, reason)
		mut(&rec)
		writeTriageReportFixture(t, root, reason, rec)
	}
	got := reportJSON(t, root)
	reasons := got["excluded_reasons"].(map[string]any)
	for reason := range cases {
		if reasons[reason] != float64(1) {
			t.Errorf("%s = %v in %v", reason, reasons[reason], reasons)
		}
	}
	if got["pairs"] != float64(0) || got["excluded"] != float64(len(cases)) || got["healthy"] != true {
		t.Errorf("%v", got)
	}
}

func TestTriageReportRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"report", "--unknown"}, {"report", "extra"}} {
		if rc := cmdTriage(args); rc != exitcode.Usage {
			t.Errorf("%v exited %d, want usage", args, rc)
		}
	}
}
