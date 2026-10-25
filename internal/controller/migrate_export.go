package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// ExportComplete is written last. A directory without it is a failed export.
const ExportComplete = "COMPLETE"

// exportDocLimit bounds how much of an artifact is read to look for the
// artifacts it names. A snapshot or patch is far smaller; anything larger is
// treated as opaque bytes.
const exportDocLimit = 64 << 20

// ExportManifest describes an export. Together with the artifacts beside it, it
// is enough to read the run's history with the source store gone.
type ExportManifest struct {
	Version    int              `json:"version"`
	RunID      string           `json:"run_id"`
	CheckoutID string           `json:"checkout_id"`
	State      string           `json:"state"`
	PauseCode  string           `json:"pause_code,omitempty"`
	Tasks      []ExportTask     `json:"tasks"`
	Attempts   []ExportAttempt  `json:"attempts"`
	Effects    []ExportEffect   `json:"effects"`
	Events     int              `json:"events"`
	Artifacts  []ExportArtifact `json:"artifacts"`
	// Unresolved lists what was still uncertain when the run was exported, so
	// an operator reading only the export does not take silence for success.
	Unresolved []string `json:"unresolved"`
}

type ExportTask struct {
	ID       string `json:"id"`
	Ordinal  int    `json:"ordinal"`
	Accepted string `json:"accepted_attempt_id,omitempty"`
}

type ExportAttempt struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Ordinal        int    `json:"ordinal"`
	Implementation string `json:"implementation"`
	Check          string `json:"check"`
	Review         string `json:"review"`
}

type ExportEffect struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	AttemptID string `json:"attempt_id,omitempty"`
}

type ExportArtifact struct {
	Hash  string   `json:"hash"`
	Size  int64    `json:"size"`
	Roles []string `json:"roles"`
}

// Export copies a run's history into a new directory: a manifest and every
// artifact its rows name, in artifact store layout. Each copy is hashed and
// read back, and the completion marker is written last. The destination must
// not exist.
//
// Export only reads. To roll back from the durable engine, stop the run, let
// its jobs settle, export it, and keep the durable store.
func (c *Controller) Export(ctx context.Context, runID, output string) (ExportManifest, error) {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil {
		return ExportManifest{}, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return ExportManifest{}, err
	}
	if _, err := os.Lstat(output); err == nil {
		return ExportManifest{}, fmt.Errorf("%w: %s already exists; an export never overwrites", workflow.ErrConflict, output)
	} else if !os.IsNotExist(err) {
		return ExportManifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return ExportManifest{}, err
	}
	if err := os.Mkdir(output, 0o755); err != nil {
		return ExportManifest{}, fmt.Errorf("%w: %v", workflow.ErrConflict, err)
	}
	dst, err := artifact.Open(output)
	if err != nil {
		return ExportManifest{}, err
	}
	// Held from the first byte read until the export is done, so maintenance
	// cannot collect what is being copied.
	locker, err := state.OpenLocker(c.d.State.Root())
	if err != nil {
		return ExportManifest{}, err
	}
	unlock, err := locker.LockShared(ctx)
	if err != nil {
		return ExportManifest{}, err
	}
	defer unlock()

	m := ExportManifest{Version: 1, RunID: runID, CheckoutID: run.CheckoutID, State: run.State, PauseCode: run.PauseCode, Unresolved: []string{}}
	tasks, err := c.d.State.RunTasks(ctx, runID)
	if err != nil {
		return m, err
	}
	for _, t := range tasks {
		m.Tasks = append(m.Tasks, ExportTask{ID: t.ID, Ordinal: t.Ordinal, Accepted: t.AcceptedAttemptID})
		attempts, err := c.d.State.TaskAttempts(ctx, runID, t.ID)
		if err != nil {
			return m, err
		}
		for _, a := range attempts {
			m.Attempts = append(m.Attempts, ExportAttempt{ID: a.ID, TaskID: a.TaskID, Ordinal: a.Ordinal,
				Implementation: string(a.Implementation), Check: string(a.Check), Review: string(a.Review)})
		}
	}
	effects, err := c.d.State.RunEffects(ctx, runID)
	if err != nil {
		return m, err
	}
	for _, x := range effects {
		m.Effects = append(m.Effects, ExportEffect{ID: x.ID, Kind: x.Kind, State: x.State, AttemptID: x.AttemptID})
		if x.State == "delivery_unknown" || x.State == "delivering" {
			m.Unresolved = append(m.Unresolved, fmt.Sprintf("effect %s (%s) is %s: whether the agent received it is not known", x.ID, x.Kind, x.State))
		}
	}
	events, err := c.d.State.RunEvents(ctx, runID)
	if err != nil {
		return m, err
	}
	m.Events = len(events)

	uses, err := c.d.State.RunArtifacts(ctx, runID)
	if err != nil {
		return m, err
	}
	if m.Artifacts, err = c.copyReachable(ctx, dst, uses); err != nil {
		return m, err
	}
	if c.hooks.beforeExportComplete != nil {
		if err := c.hooks.beforeExportComplete(); err != nil {
			return m, err
		}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	raw = append(raw, '\n')
	if err := writeFileSync(filepath.Join(output, "manifest.json"), raw); err != nil {
		return m, err
	}
	sum := sha256.Sum256(raw)
	if err := writeFileSync(filepath.Join(output, ExportComplete), []byte(hex.EncodeToString(sum[:])+"\n")); err != nil {
		return m, err
	}
	return m, nil
}

// copyReachable copies the listed artifacts and, transitively, the artifacts
// their bytes name.
func (c *Controller) copyReachable(ctx context.Context, dst *artifact.Store, uses []state.ArtifactUse) ([]ExportArtifact, error) {
	var out []ExportArtifact
	seen := map[string]bool{}
	type item struct {
		hash  string
		roles []string
	}
	var queue []item
	for _, u := range uses {
		seen[u.Hash] = true
		queue = append(queue, item{u.Hash, u.Roles})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		size, err := c.copyOne(ctx, dst, it.hash)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", it.hash, err)
		}
		out = append(out, ExportArtifact{Hash: it.hash, Size: size, Roles: it.roles})
		if size > exportDocLimit {
			continue
		}
		raw, err := c.d.Artifacts.ReadAll(ctx, it.hash, exportDocLimit)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", it.hash, err)
		}
		for _, child := range snapshot.Children(raw) {
			if !seen[child] {
				seen[child] = true
				queue = append(queue, item{child, []string{"content of " + it.hash[:12]}})
			}
		}
	}
	return out, nil
}

// copyOne streams one artifact into dst. The source verifies every chunk it
// returns, so a corrupt source fails here, and dst hashes what it stores; the
// stored copy is then read back, which verifies it once more.
func (c *Controller) copyOne(ctx context.Context, dst *artifact.Store, hash string) (int64, error) {
	if _, err := dst.Put(ctx, &chunkReader{ctx: ctx, s: c.d.Artifacts, hash: hash}); err != nil {
		return 0, err
	}
	chunk, err := dst.Read(ctx, hash, 0, 1)
	if err != nil {
		return 0, err
	}
	return chunk.TotalBytes, nil
}

// chunkReader reads an artifact in bounded, verified chunks.
type chunkReader struct {
	ctx    context.Context
	s      *artifact.Store
	hash   string
	offset int64
	buf    []byte
	done   bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.done {
			return 0, io.EOF
		}
		ch, err := r.s.Read(r.ctx, r.hash, r.offset, artifact.MaxLimit)
		if err != nil {
			return 0, err
		}
		r.buf = ch.Data
		if ch.NextOffset == nil {
			r.done = true
		} else {
			r.offset = *ch.NextOffset
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func writeFileSync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
