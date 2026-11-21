package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/check"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

const gateBaselineUse = "usage: tandem gate baseline-check --root DIR --command CMD [--relay DIR] [--cached-only] [--fields]"

type baselineCheckRecord struct {
	TreeHash   string `json:"tree_hash"`
	Command    string `json:"command"`
	Exit       int    `json:"exit"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
	Log        string `json:"log"`
	Summary    string `json:"summary"`
}

type baselineCheckOut struct {
	Status   string `json:"status"`
	Exit     int    `json:"exit"`
	TimedOut bool   `json:"timed_out,omitempty"`
	TreeHash string `json:"tree_hash,omitempty"`
	Log      string `json:"log,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

// cmdGateBaselineCheck runs the project's check command before a whole-plan
// review (or reuses a cached result recorded for the same working-tree hash
// and check command), so the reviewer does not have to run the full check.
func cmdGateBaselineCheck(args []string) int {
	fs := flag.NewFlagSet("gate baseline-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	command := fs.String("command", "", "project check command")
	relay := fs.String("relay", "", "relay mailbox directory (default: paths.Relay(root))")
	cachedOnly := fs.Bool("cached-only", false, "only report a cached result; do not run the check on miss")
	asFields := fs.Bool("fields", false, "print NUL-separated fields (status, exit, tree_hash, log, summary)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *root == "" || *command == "" {
		fmt.Fprintln(os.Stderr, gateBaselineUse)
		return exitcode.Usage
	}
	if *relay == "" {
		*relay = paths.Relay(*root)
	}

	treeHash := baselineTreeHash(*root)
	cachePath := filepath.Join(*relay, "baseline-check.json")
	logPath := filepath.Join(*relay, "baseline-check.log")

	if treeHash != "" {
		if rec, ok := readBaselineCache(cachePath); ok && rec.TreeHash == treeHash && rec.Command == *command {
			return emitBaselineResult(*asFields, "cached", rec)
		}
	}
	if *cachedOnly {
		if *asFields {
			writeFields(os.Stdout, "miss", "", treeHash, "", "")
			return exitcode.OK
		}
		return printJSON(baselineCheckOut{Status: "miss", TreeHash: treeHash})
	}

	timeout := 15 * time.Minute
	if raw := os.Getenv("TANDEM_CHECK_TIMEOUT_S"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	res, err := check.Run(ctx, *root, *command, nil, logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tandem gate baseline-check:", err)
		return exitcode.Internal
	}
	summary, err := check.SummarizeFile(res, logPath, check.MaxSummaryBytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tandem gate baseline-check:", err)
		return exitcode.Internal
	}
	rec := baselineCheckRecord{
		TreeHash:   treeHash,
		Command:    *command,
		Exit:       res.Exit,
		TimedOut:   res.TimedOut,
		DurationMS: res.Duration.Milliseconds(),
		Log:        logPath,
		Summary:    summary,
	}
	if err := writeFileAtomic(cachePath, rec); err != nil {
		fmt.Fprintln(os.Stderr, "tandem gate baseline-check: cache write:", err)
	}
	return emitBaselineResult(*asFields, "fresh", rec)
}

func emitBaselineResult(asFields bool, status string, rec baselineCheckRecord) int {
	if asFields {
		writeFields(os.Stdout, status, strconv.Itoa(rec.Exit), rec.TreeHash, rec.Log, rec.Summary)
		return exitcode.OK
	}
	return printJSON(baselineCheckOut{
		Status:   status,
		Exit:     rec.Exit,
		TimedOut: rec.TimedOut,
		TreeHash: rec.TreeHash,
		Log:      rec.Log,
		Summary:  rec.Summary,
	})
}

func readBaselineCache(path string) (baselineCheckRecord, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return baselineCheckRecord{}, false
	}
	var rec baselineCheckRecord
	if err := json.Unmarshal(b, &rec); err != nil || rec.TreeHash == "" || rec.Command == "" {
		return baselineCheckRecord{}, false
	}
	return rec, true
}

// baselineTreeHash computes a git tree hash of the current working tree
// (tracked and untracked non-ignored files) using a temporary index file, so
// the repository's real index is never touched. Returns "" outside git.
func baselineTreeHash(root string) string {
	tmpDir, err := os.MkdirTemp("", "tandem-tree-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(tmpDir)
	idx := filepath.Join(tmpDir, "index")
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)

	add := exec.Command("git", "-C", root, "add", "-A")
	add.Env = env
	if err := add.Run(); err != nil {
		return ""
	}
	wt := exec.Command("git", "-C", root, "write-tree")
	wt.Env = env
	out, err := wt.Output()
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(out))
	if len(h) < 40 {
		return ""
	}
	return h
}
