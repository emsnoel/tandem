// Package controller carries out the durable runner's trusted steps. Its
// checks are run by the controller, not the builder: the process, its log, the
// fingerprints of what it ran on and the certificate that records the outcome
// are all produced here, so nothing a builder reports can supply a check's
// result.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/check"
)

var (
	idPattern  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// ErrSourceMutated: the check changed the working tree it ran against, so
	// it cannot certify the tree it left. It also reads as incomplete evidence.
	ErrSourceMutated = errors.New("the check changed the working tree")
)

// CheckInputs declares inputs beyond the tracked and untracked files a
// snapshot already covers. Values are fingerprinted, never stored.
type CheckInputs struct {
	// Env names environment variables whose values the check depends on.
	Env []string `json:"env"`
	// Files are repository-relative paths, ignored or not, the check reads.
	Files []string `json:"files"`
	// Tools are commands (resolved on PATH) or paths whose bytes the check runs.
	Tools []string `json:"tools"`
}

// CheckRequest describes one check run. JobID names the job's log and every
// artifact it produces, so the job exists before the process starts.
type CheckRequest struct {
	AttemptID string
	JobID     string
	Root      string
	Command   string
	Args      []string
	// Cwd is relative to Root; empty means Root itself.
	Cwd    string
	Inputs CheckInputs
}

// CheckResult is what a check run established. Valid is the only question
// acceptance may ask of it.
type CheckResult struct {
	JobID, AttemptID string
	Exit             int
	TimedOut         bool
	BeforeInputsHash string
	AfterInputsHash  string
	CommandHash      string
	EnvironmentHash  string
	LogHash          string
	// Summary is at most check.MaxSummaryBytes, redacted, and names LogHash.
	Summary string
	// Complete is true when the inputs were captured exactly before and after.
	Complete bool
	Errors   []string
}

// Mutated reports that the inputs differ before and after the run.
func (r CheckResult) Mutated() bool { return r.BeforeInputsHash != r.AfterInputsHash }

// Valid reports whether the run may certify its tree for acceptance or reuse:
// it passed, its inputs were captured completely, and the check did not change
// them.
func (r CheckResult) Valid() bool { return r.Exit == 0 && r.Complete && !r.Mutated() }

// commandDoc and the other *Doc types are canonical, typed encodings: no maps,
// so equal inputs hash equal.
type commandDoc struct {
	Version int      `json:"version"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
}

type envEntry struct {
	Name      string `json:"name"`
	ValueHash string `json:"value_hash"`
}

type toolEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type environmentDoc struct {
	Version int         `json:"version"`
	Env     []envEntry  `json:"env"`
	Tools   []toolEntry `json:"tools"`
}

type fileEntry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type inputsDoc struct {
	Version          int         `json:"version"`
	SnapshotHash     string      `json:"snapshot_hash"`
	SnapshotComplete bool        `json:"snapshot_complete"`
	CommandHash      string      `json:"command_hash"`
	EnvironmentHash  string      `json:"environment_hash"`
	Files            []fileEntry `json:"files"`
	Tools            []toolEntry `json:"tools"`
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // the document types cannot fail to encode
	return buf.Bytes()
}

func putDoc(ctx context.Context, art *artifact.Store, v any) (string, error) {
	return art.Put(ctx, bytes.NewReader(encode(v)))
}

// NewCheckExecutor returns the function that runs a check: it fingerprints the
// inputs, runs the command in its own process group under ctx, imports the full
// log as an artifact whether or not the command passed, fingerprints the inputs
// again, and bounds the summary. It never accepts a task. A cancelled ctx stops
// the whole process group and is returned as an error; an expired deadline is a
// timed-out result.
func NewCheckExecutor(artifacts *artifact.Store) func(context.Context, CheckRequest) (CheckResult, error) {
	return func(ctx context.Context, req CheckRequest) (CheckResult, error) {
		return runCheck(ctx, artifacts, req)
	}
}

func runCheck(ctx context.Context, art *artifact.Store, req CheckRequest) (CheckResult, error) {
	root, cwd, rel, err := req.resolve()
	if err != nil {
		return CheckResult{}, err
	}
	// The post-run steps must finish even when the deadline that stopped the
	// command has passed.
	post := context.WithoutCancel(ctx)

	cmdHash, err := putDoc(post, art, commandDoc{Version: 1, Command: req.Command, Args: nonNil(req.Args), Cwd: rel})
	if err != nil {
		return CheckResult{}, err
	}
	env, err := environment(req.Inputs)
	if err != nil {
		return CheckResult{}, err
	}
	envHash, err := putDoc(post, art, environmentDoc{Version: 1, Env: env.env, Tools: env.tools})
	if err != nil {
		return CheckResult{}, err
	}

	before, bComplete, bErrs, err := fingerprint(post, art, root, req.Inputs, cmdHash, envHash)
	if err != nil {
		return CheckResult{}, err
	}

	logPath := filepath.Join(art.ScratchDir(), req.JobID+".log")
	if _, err := os.Lstat(logPath); err == nil {
		return CheckResult{}, fmt.Errorf("the log for job %s already exists; a job runs once", req.JobID)
	}
	defer os.Remove(logPath)

	r, err := check.Run(ctx, cwd, req.Command, req.Args, logPath)
	if err != nil {
		return CheckResult{}, fmt.Errorf("starting the check: %w", err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return CheckResult{}, ctx.Err()
	}

	after, aComplete, aErrs, err := fingerprint(post, art, root, req.Inputs, cmdHash, envHash)
	if err != nil {
		return CheckResult{}, err
	}
	f, err := os.Open(logPath)
	if err != nil {
		return CheckResult{}, err
	}
	logHash, err := art.Put(post, f)
	f.Close()
	if err != nil {
		return CheckResult{}, fmt.Errorf("importing the check log: %w", err)
	}
	summary, err := check.SummarizeFile(r, logHash, check.MaxSummaryBytes)
	if err != nil {
		return CheckResult{}, err
	}

	res := CheckResult{JobID: req.JobID, AttemptID: req.AttemptID, Exit: r.Exit, TimedOut: r.TimedOut,
		BeforeInputsHash: before, AfterInputsHash: after, CommandHash: cmdHash, EnvironmentHash: envHash,
		LogHash: logHash, Summary: summary, Complete: bComplete && aComplete, Errors: []string{}}
	for _, e := range bErrs {
		res.Errors = append(res.Errors, "before the check: "+e)
	}
	for _, e := range aErrs {
		res.Errors = append(res.Errors, "after the check: "+e)
	}
	if res.Mutated() {
		res.Errors = append(res.Errors, "the check changed its own inputs: the working tree differs before and after, so it does not certify the tree it left")
	}
	return res, nil
}
