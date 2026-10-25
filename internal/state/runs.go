package state

import (
	"path/filepath"
	"strings"
)

// CheckoutID is the id of the checkout this store belongs to: the name of its
// state root directory, assigned by the registry.
func (s *Store) CheckoutID() string { return filepath.Base(filepath.Dir(s.path)) }

// Root is the checkout's state root: the directory holding the database and
// the artifact store.
func (s *Store) Root() string { return filepath.Dir(s.path) }

// GitIdentity returns the canonical worktree root and git common directory for
// cwd, the same canonicalization the registry binds checkouts by.
func GitIdentity(cwd string) (root, common string, err error) { return gitIdentity(cwd) }

// TaskRow is one task of a run as stored: its brief, constraints and spec are
// artifacts, and DependsOn names tasks that must be accepted first.
type TaskRow struct {
	ID                                   string
	Ordinal                              int
	BriefHash, ConstraintsHash, SpecHash string
	DependsOn                            []string
	AcceptedAttemptID                    string
}

// RunSpec is everything CreateRun records.
type RunSpec struct {
	RunID                                   string
	CheckoutID, CanonicalRoot, GitCommonDir string
	ManifestRoot                            string
	PlanHash, ConfigHash                    string
	TaskInputLimit, RunInputLimit           int64
	Tasks                                   []TaskRow
	// Imported are tasks a legacy engine had already finished. Each is recorded
	// with one accepted attempt that has no snapshot, patch, check or review:
	// the acceptance is the legacy engine's claim, and what it never captured
	// stays unresolved.
	Imported []ImportedTask
}

// ImportedTask is one task carried over from a legacy build.
type ImportedTask struct {
	TaskID, AttemptID string
	// Refs are the preserved legacy evidence the attempt points at, as {role, hash}.
	Refs [][2]string
}

// Run is a stored run.
type Run struct {
	ID, ProjectID, CheckoutID, State, PauseCode string
	PlanHash, ConfigHash                        string
	Version                                     int64
	// TaskInputLimit and RunInputLimit are the run's persisted budget limits, in
	// total logical input tokens; 0 is no limit. They belong to the run, not to
	// the process or environment that reads them.
	TaskInputLimit, RunInputLimit int64
	// PauseGeneration counts pauses and StopGeneration counts stops. PauseJobID
	// and PauseEffectID name what a review_unknown pause waits on.
	PauseGeneration, StopGeneration int64
	PauseJobID, PauseEffectID       string
}

// AttemptSpec creates an attempt: pending and current, at version 0.
type AttemptSpec struct {
	ID, RunID, TaskID, ParentID   string
	Ordinal                       int
	StartSnapshotHash, PacketHash string
	// ManifestHash optionally names the context manifest the packet was built
	// with: what it contains, what it left out and why.
	ManifestHash string
	// Refs are further artifacts the attempt may read, as {role, hash}: for a
	// repair, the review that asked for it.
	Refs [][2]string
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
