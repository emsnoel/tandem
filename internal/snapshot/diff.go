package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Change operations.
const (
	OpAdd    = "add"
	OpDelete = "delete"
	// OpModify is a different kind or different content (and possibly mode).
	OpModify = "modify"
	// OpMode is a changed executable bit with identical content.
	OpMode = "mode"
)

// Change is one path that differs between two snapshots, with references to
// the stored before and after content. A rename is a delete plus an add.
type Change struct {
	pathed
	Op               string `json:"op"`
	BeforeKind       string `json:"before_kind,omitempty"`
	AfterKind        string `json:"after_kind,omitempty"`
	BeforeExecutable bool   `json:"before_executable"`
	AfterExecutable  bool   `json:"after_executable"`
	BeforeHash       string `json:"before_hash,omitempty"`
	AfterHash        string `json:"after_hash,omitempty"`
	BeforeSize       int64  `json:"before_size"`
	AfterSize        int64  `json:"after_size"`
}

// Entry returns the changed path.
func (c Change) Entry() string { return c.PathString() }

// Patch is the exact difference between two snapshots. Complete is false, with
// Errors, when either snapshot was: an incomplete patch is evidence of nothing
// and is never an empty successful one.
type Patch struct {
	StartSnapshotHash string   `json:"start_snapshot_hash"`
	EndSnapshotHash   string   `json:"end_snapshot_hash"`
	Hash              string   `json:"hash"`
	Complete          bool     `json:"complete"`
	Errors            []string `json:"errors"`
	Changes           []Change `json:"changes"`
}

type patchDoc struct {
	Version           int      `json:"version"`
	StartSnapshotHash string   `json:"start_snapshot_hash"`
	EndSnapshotHash   string   `json:"end_snapshot_hash"`
	Complete          bool     `json:"complete"`
	Errors            []string `json:"errors"`
	Changes           []Change `json:"changes"`
}

// Diff compares two manifests over the union of their sorted paths. It is
// pure: it reads only its arguments. The error is for malformed manifests; an
// incomplete input yields a Patch with Complete=false.
func Diff(before, after Manifest) (Patch, error) {
	if err := before.validate(); err != nil {
		return Patch{}, fmt.Errorf("start snapshot: %w", err)
	}
	if err := after.validate(); err != nil {
		return Patch{}, fmt.Errorf("end snapshot: %w", err)
	}
	p := Patch{StartSnapshotHash: before.Hash, EndSnapshotHash: after.Hash, Complete: before.Complete && after.Complete, Errors: []string{}, Changes: []Change{}}
	for _, e := range before.Errors {
		p.Errors = append(p.Errors, "start snapshot: "+e)
	}
	for _, e := range after.Errors {
		p.Errors = append(p.Errors, "end snapshot: "+e)
	}
	if !p.Complete && len(p.Errors) == 0 {
		p.Errors = append(p.Errors, "a snapshot is marked incomplete")
	}

	i, j := 0, 0
	for i < len(before.Entries) || j < len(after.Entries) {
		var b, a *Entry
		switch {
		case i >= len(before.Entries):
			a = &after.Entries[j]
			j++
		case j >= len(after.Entries):
			b = &before.Entries[i]
			i++
		default:
			switch c := bytes.Compare(before.Entries[i].RawPath(), after.Entries[j].RawPath()); {
			case c < 0:
				b = &before.Entries[i]
				i++
			case c > 0:
				a = &after.Entries[j]
				j++
			default:
				b, a = &before.Entries[i], &after.Entries[j]
				i, j = i+1, j+1
			}
		}
		if ch, ok := compare(b, a); ok {
			p.Changes = append(p.Changes, ch)
		}
	}
	body, hash, err := canonical(patchDoc{Version: 1, StartSnapshotHash: p.StartSnapshotHash, EndSnapshotHash: p.EndSnapshotHash,
		Complete: p.Complete, Errors: p.Errors, Changes: p.Changes})
	if err != nil {
		return Patch{}, err
	}
	_ = body
	p.Hash = hash
	return p, nil
}

func compare(b, a *Entry) (Change, bool) {
	var c Change
	switch {
	case b == nil:
		c = Change{pathed: a.pathed, Op: OpAdd, AfterKind: a.Kind, AfterExecutable: a.Executable, AfterHash: a.ContentHash, AfterSize: a.Size}
	case a == nil:
		c = Change{pathed: b.pathed, Op: OpDelete, BeforeKind: b.Kind, BeforeExecutable: b.Executable, BeforeHash: b.ContentHash, BeforeSize: b.Size}
	default:
		if b.Kind == a.Kind && b.ContentHash == a.ContentHash && b.Executable == a.Executable {
			return Change{}, false
		}
		c = Change{pathed: a.pathed,
			BeforeKind: b.Kind, AfterKind: a.Kind, BeforeExecutable: b.Executable, AfterExecutable: a.Executable,
			BeforeHash: b.ContentHash, AfterHash: a.ContentHash, BeforeSize: b.Size, AfterSize: a.Size}
		if b.Kind == a.Kind && b.ContentHash == a.ContentHash {
			c.Op = OpMode
		} else {
			c.Op = OpModify
		}
	}
	return c, true
}

// StorePatch stores a complete patch's canonical encoding as an artifact and
// returns its hash, which is the patch's own Hash. An incomplete patch is
// refused so that it can never be mistaken for evidence.
func StorePatch(ctx context.Context, store *artifact.Store, p Patch) (string, error) {
	if !p.Complete {
		return "", fmt.Errorf("%w: the patch is incomplete: %s", workflow.ErrIncompleteEvidence, strings.Join(p.Errors, "; "))
	}
	body, hash, err := canonical(patchDoc{Version: 1, StartSnapshotHash: p.StartSnapshotHash, EndSnapshotHash: p.EndSnapshotHash,
		Complete: p.Complete, Errors: p.Errors, Changes: p.Changes})
	if err != nil {
		return "", err
	}
	got, err := store.Put(ctx, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	if got != hash || got != p.Hash {
		return "", fmt.Errorf("the patch hash %s does not match its canonical encoding %s", p.Hash, got)
	}
	return got, nil
}
