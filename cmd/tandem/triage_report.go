package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/triage"
	"github.com/emsnoel/tandem/internal/vcs"
)

func cmdTriage(args []string) int {
	if len(args) == 0 || args[0] != "report" {
		fmt.Fprintln(os.Stderr, "usage: tandem triage report [--root DIR] [--json]")
		return exitcode.Usage
	}
	return cmdTriageReport(args[1:])
}

func cmdTriageReport(args []string) int {
	fs := flag.NewFlagSet("triage report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root (default: this git checkout)")
	asJSON := fs.Bool("json", false, "print counts and gate_open as JSON")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem triage report [--root DIR] [--json]")
		return exitcode.Usage
	}
	r := *root
	if r == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fail(err)
		}
		r, err = vcs.Toplevel(wd)
		if err != nil {
			return fail(fmt.Errorf("not inside a git checkout; pass --root"))
		}
	}
	logDir := filepath.Join(r, ".tandem-log")
	rows, corrupt, err := loadRows(logDir)
	if err != nil {
		return fail(err)
	}
	report := triage.Summarize(rows, triage.CurrentCohort(currentReviewerConfig(r)), corrupt)
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			triage.Report
			GateOpen bool `json:"gate_open"`
		}{report, report.Passes()}); err != nil {
			return fail(err)
		}
		return exitcode.OK
	}
	fmt.Printf("pairs: %d   would-skip pairs: %d   reviewer changed the tree: %d   misses: %d   excluded: %d\n",
		report.Pairs, report.SkipPairs, report.ReviewerChanged, report.Misses, report.Excluded)
	fmt.Printf("health: %s   missing pairs: %d   audit pairs: %d (misses %d)\n", healthText(report), report.MissingPairs, report.AuditPairs, report.AuditMisses)
	switch {
	case report.Passes():
		fmt.Println("gate: open")
	case !report.Healthy:
		fmt.Printf("gate: closed (%d calibration row(s) are corrupt or unreadable; the evidence cannot be trusted until they are fixed or removed)\n", report.Corrupt)
	default:
		fmt.Println("gate: closed (needs 30 would-skip pairs, 10 pairs with a change, 0 misses)")
	}
	for _, reason := range reasonKeys(report) {
		fmt.Printf("  excluded %s: %d\n", reason, report.ExcludedReasons[reason])
	}
	fmt.Println("Zero misses in 30 still allows a true miss rate near 10%.")
	fmt.Println("One miss under the current contract, policy and model keeps the gate closed until one of them changes. It does not reopen on later clean rows.")
	for _, n := range report.Notes {
		fmt.Println("note:", n)
	}
	return exitcode.OK
}

func healthText(r triage.Report) string {
	if r.Healthy {
		return "healthy"
	}
	return fmt.Sprintf("UNHEALTHY (%d corrupt)", r.Corrupt)
}

func reasonKeys(r triage.Report) []string {
	var keys []string
	for k := range r.ExcludedReasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// loadRows shares the calibration evidence between the report and build loop. A
// row that cannot be read, names another job than its file, or refers to input
// that is missing or does not match its hash is counted as corrupt and not
// dropped: evidence that cannot be read whole must close the gate, not shrink
// the sample. Atomic-write temporary files do not match, and rows written before
// the evidence schema are legacy rows, not corrupt ones.
func loadRows(logDir string) (rows []triage.Row, corrupt int, err error) {
	paths, err := filepath.Glob(filepath.Join(logDir, "judge-*.json"))
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(paths)
	for _, path := range paths {
		base := filepath.Base(path)
		if strings.HasPrefix(base, "judge-input-") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			corrupt++
			continue
		}
		var rec judgeRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			corrupt++
			continue
		}
		if rec.ID == "" || rec.ID != strings.TrimSuffix(strings.TrimPrefix(base, "judge-"), ".json") {
			corrupt++
			continue
		}
		if rec.Identity.InputHash != "" && !verifyJudgeInput(logDir, rec.Identity.InputHash) {
			corrupt++
			continue
		}
		job := rec.JobID
		if job == "" {
			job = rec.ID
		}
		rows = append(rows, triage.Row{
			JobID: job, Verdict: rec.Verdict, WouldSkip: rec.WouldSkip,
			Reviewed: rec.Reviewed, ReviewChangedTree: rec.ReviewChangedTree, Truncated: rec.Truncated,
			JudgedTree: rec.JudgedTree, ReviewedTree: rec.ReviewedTree, AuditSelected: rec.AuditSelected, Identity: rec.Identity,
		})
	}
	return rows, corrupt, nil
}
