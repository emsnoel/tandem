package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// CheckSpec is what StartCheck needs to allocate a check job.
type CheckSpec struct {
	AttemptID string
	// InputHash is the frozen end snapshot the check examines.
	InputHash string
	Root      string
	Command   string
	Args      []string
	Cwd       string
	Inputs    CheckInputs
}

type configDoc struct {
	Version int         `json:"version"`
	Command string      `json:"command"`
	Args    []string    `json:"args"`
	Cwd     string      `json:"cwd"`
	Inputs  CheckInputs `json:"inputs"`
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// StartCheck allocates the check's job before anything launches: the declared
// configuration is stored as an immutable artifact, the job row names it, and
// the returned request carries the job id that names the log.
func StartCheck(ctx context.Context, st *state.Store, art *artifact.Store, spec CheckSpec) (CheckRequest, error) {
	req := CheckRequest{AttemptID: spec.AttemptID, Root: spec.Root, Command: spec.Command, Args: spec.Args, Cwd: spec.Cwd, Inputs: spec.Inputs}
	id, err := randomID()
	if err != nil {
		return CheckRequest{}, err
	}
	req.JobID = id
	if _, _, _, err := req.resolve(); err != nil {
		return CheckRequest{}, err
	}
	cwd := spec.Cwd
	if cwd == "" {
		cwd = "."
	}
	cfg, err := putDoc(ctx, art, configDoc{Version: 1, Command: spec.Command, Args: nonNil(spec.Args), Cwd: filepath.ToSlash(filepath.Clean(cwd)),
		Inputs: CheckInputs{Env: nonNil(spec.Inputs.Env), Files: nonNil(spec.Inputs.Files), Tools: nonNil(spec.Inputs.Tools)}})
	if err != nil {
		return CheckRequest{}, err
	}
	if err := st.AdoptArtifact(ctx, cfg); err != nil {
		return CheckRequest{}, err
	}
	if err := st.InsertJob(ctx, workflow.Job{ID: id, AttemptID: spec.AttemptID, Kind: "check", State: "running",
		InputHash: spec.InputHash, ConfigHash: cfg, RunnerKind: "local-check"}); err != nil {
		return CheckRequest{}, err
	}
	return req, nil
}

// RecordSpec identifies the event that reports a finished check.
type RecordSpec struct {
	RunID string
	// ExpectedVersion is the attempt's version the controller read.
	ExpectedVersion int64
	// EventID is chosen by the caller and reused on retry, which makes the
	// recording idempotent.
	EventID string
}

// RecordCheck persists the certificate for a finished check, completing its
// job, then applies check_completed. The event names no outcome: the store
// reads the certificate back. A check that changed its own inputs is recorded
// but not applied; it returns ErrSourceMutated so the controller can pause and
// ask for a clean verification pass instead of looping. Retrying with the same
// spec after a partial failure is safe.
func RecordCheck(ctx context.Context, st *state.Store, art *artifact.Store, spec RecordSpec, res CheckResult, obs workflow.Observed) (workflow.Receipt, error) {
	for _, h := range []string{res.BeforeInputsHash, res.AfterInputsHash, res.CommandHash, res.EnvironmentHash, res.LogHash} {
		if err := st.AdoptArtifact(ctx, h); err != nil {
			return workflow.Receipt{}, err
		}
	}
	sum := sha256.Sum256([]byte("check-certificate|" + res.JobID))
	cert := state.CheckCertificate{ID: hex.EncodeToString(sum[:16]), AttemptID: res.AttemptID, JobID: res.JobID,
		BeforeInputsHash: res.BeforeInputsHash, AfterInputsHash: res.AfterInputsHash, CommandHash: res.CommandHash,
		EnvironmentHash: res.EnvironmentHash, LogHash: res.LogHash, ExitCode: res.Exit, Complete: res.Complete}
	if err := st.RecordCheckCertificate(ctx, cert); err != nil {
		// A retry finds its own certificate already stored; anything else that
		// conflicts is a real conflict.
		stored, ok, gerr := st.GetCheckCertificate(ctx, cert.ID)
		if !errors.Is(err, workflow.ErrConflict) || gerr != nil || !ok || stored != cert {
			return workflow.Receipt{}, err
		}
	}
	if res.Mutated() {
		return workflow.Receipt{}, fmt.Errorf("%w; %w: %s", ErrSourceMutated, workflow.ErrIncompleteEvidence, strings.Join(res.Errors, "; "))
	}
	payload, err := putDoc(ctx, art, struct {
		JobID     string `json:"job_id"`
		AttemptID string `json:"attempt_id"`
		Summary   string `json:"summary"`
	}{res.JobID, res.AttemptID, res.Summary})
	if err != nil {
		return workflow.Receipt{}, err
	}
	if err := st.AdoptArtifact(ctx, payload); err != nil {
		return workflow.Receipt{}, err
	}
	return st.ApplyWith(ctx, workflow.Event{ID: spec.EventID, RunID: spec.RunID, AttemptID: res.AttemptID,
		Kind: workflow.EventCheckCompleted, PayloadHash: payload, ExpectedVersion: spec.ExpectedVersion}, obs)
}
