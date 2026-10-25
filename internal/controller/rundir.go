package controller

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ActiveRun is the marker that a relay carries a durable run. Its presence is
// what routes every entrypoint to protocol 2; a legacy build.json is never
// written for such a run, so old handback code cannot mistake it for one.
type ActiveRun struct {
	EngineVersion   int    `json:"engine_version"`
	ProtocolVersion int    `json:"protocol_version"`
	RunID           string `json:"run_id"`
	ProjectID       string `json:"project_id"`
}

// ManifestTask maps a task's position to its stable id.
type ManifestTask struct {
	Ordinal int    `json:"ordinal"`
	ID      string `json:"id"`
}

// RunManifest is the copy of the run's task list the builder-side helper uses
// to resolve `--task N`.
type RunManifest struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	// ReviewProtocol is the review protocol the run was started with, copied
	// from its pinned configuration so the builder-side helper, which cannot
	// reach the state, writes the message version the run speaks. Omitted at
	// the default.
	ReviewProtocol int            `json:"review_protocol,omitempty"`
	Tasks          []ManifestTask `json:"tasks"`
}

// RunDir is a run's directory under a relay.
func RunDir(relay, runID string) string { return filepath.Join(relay, "durable", runID) }

// InitRunDir creates a run's mailbox directories and manifest, then writes the
// active-run marker last, so the marker means the run is ready.
func InitRunDir(relay string, run ActiveRun, manifest RunManifest) (string, error) {
	if !idPattern.MatchString(run.RunID) || !idPattern.MatchString(run.ProjectID) {
		return "", errors.New("run and project ids must be 32 lowercase hex characters")
	}
	if manifest.RunID != run.RunID {
		return "", errors.New("the manifest names another run")
	}
	for _, t := range manifest.Tasks {
		if !taskPattern.MatchString(t.ID) || t.Ordinal < 1 {
			return "", fmt.Errorf("manifest task %+v is invalid", t)
		}
	}
	dir := RunDir(relay, run.RunID)
	for _, sub := range []string{"q", "payloads", "receipts", "quarantine", "responses"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "", err
		}
	}
	manifest.SchemaVersion = 2
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := atomicWrite(dir, "manifest.json", append(body, '\n')); err != nil {
		return "", err
	}
	run.EngineVersion, run.ProtocolVersion = 2, 2
	body, err = json.MarshalIndent(run, "", "  ")
	if err != nil {
		return "", err
	}
	return dir, atomicWrite(relay, ActiveRunFile, append(body, '\n'))
}

// ReadActiveRun returns the relay's durable-run marker, or nil when there is
// none. A marker that cannot be read is an error: it must never read as "no
// durable run", which would send a durable run's messages down the legacy route.
func ReadActiveRun(relay string) (*ActiveRun, error) {
	raw, err := os.ReadFile(filepath.Join(relay, ActiveRunFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var run ActiveRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", ActiveRunFile, err)
	}
	if run.EngineVersion != 2 || run.ProtocolVersion != 2 || !idPattern.MatchString(run.RunID) {
		return nil, fmt.Errorf("%s names an engine or protocol this tandem does not speak (it supports engine 2, protocol 2)", ActiveRunFile)
	}
	return &run, nil
}

// ReadManifest reads a run's manifest.
func ReadManifest(runDir string) (RunManifest, error) {
	raw, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		return RunManifest{}, err
	}
	var m RunManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return RunManifest{}, fmt.Errorf("the run manifest is corrupt: %w", err)
	}
	return m, nil
}

// ResolveTask maps a task number to its stable id through the run manifest.
func ResolveTask(runDir string, n int) (string, error) {
	m, err := ReadManifest(runDir)
	if err != nil {
		return "", err
	}
	for _, t := range m.Tasks {
		if t.Ordinal == n {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("the run has no task %d", n)
}

// NewEventID returns a random 32 character hex event id.
func NewEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
