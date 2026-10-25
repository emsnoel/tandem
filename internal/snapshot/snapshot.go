// Package snapshot captures exactly what a checkout holds at a moment and
// diffs two captures. A task's patch is the diff between its start and end
// snapshots, so work already in the tree is never attributed to it (unlike
// internal/vcs.TaskDiff, which is legacy-only).
//
// A snapshot covers every tracked file and every untracked, non-ignored file:
// path, kind, executable bit, size and content hash, with the bytes stored as
// artifacts. A file written while it is read is retried once; a capture that
// still cannot be made consistent is reported incomplete, never as an empty
// patch.
package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/artifact"
)

// Entry kinds.
const (
	KindFile    = "file"
	KindSymlink = "symlink"
	// KindGitlink is a submodule. It is a boundary: only the commit its working
	// tree is at is recorded, never what is inside.
	KindGitlink = "gitlink"
)

// pathed holds a path losslessly. A path that is valid UTF-8 is stored as
// text; any other byte sequence is stored base64 encoded, never with
// replacement characters.
type pathed struct {
	Path    string `json:"path,omitempty"`
	PathB64 string `json:"path_b64,omitempty"`
}

func newPathed(raw []byte) pathed {
	if utf8.Valid(raw) {
		return pathed{Path: string(raw)}
	}
	return pathed{PathB64: base64.StdEncoding.EncodeToString(raw)}
}

// RawPath returns the path's exact bytes.
func (p pathed) RawPath() []byte {
	if p.PathB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(p.PathB64)
		if err == nil {
			return raw
		}
	}
	return []byte(p.Path)
}

// PathString is the path for display. For a non-UTF8 path it is the raw bytes
// as a Go string, so callers that need exactness use RawPath.
func (p pathed) PathString() string { return string(p.RawPath()) }

// Entry is one captured path. Paths are slash separated and relative to the
// repository root.
type Entry struct {
	pathed
	Kind       string `json:"kind"`
	Executable bool   `json:"executable"`
	// ContentHash is the sha256 of the file's bytes, of a symlink's target text,
	// or of "gitlink <commit>\n". Each is stored as an artifact.
	ContentHash string `json:"content_hash"`
	Size        int64  `json:"size"`
}

func newEntry(raw []byte) Entry { return Entry{pathed: newPathed(raw)} }

// Manifest is a captured snapshot. Hash names the stored canonical manifest
// artifact. Complete is false, with Errors saying why, whenever anything could
// not be captured exactly; such a manifest must not be used as evidence.
type Manifest struct {
	Hash     string   `json:"hash"`
	Complete bool     `json:"complete"`
	Errors   []string `json:"errors"`
	Entries  []Entry  `json:"entries"`
}

// manifestDoc is the canonical encoding that Hash covers: typed structs, no
// maps, entries sorted by raw path bytes.
type manifestDoc struct {
	Version  int      `json:"version"`
	Complete bool     `json:"complete"`
	Errors   []string `json:"errors"`
	Entries  []Entry  `json:"entries"`
}

// canonical returns the document bytes: JSON ending in exactly one newline,
// which is also what the artifact store hashes.
func canonical(v any) ([]byte, string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].RawPath(), entries[j].RawPath()) < 0 })
}

// validate checks the invariants Diff relies on.
func (m Manifest) validate() error {
	for i, e := range m.Entries {
		if len(e.RawPath()) == 0 {
			return fmt.Errorf("manifest entry %d has no path", i)
		}
		if e.Kind != KindFile && e.Kind != KindSymlink && e.Kind != KindGitlink {
			return fmt.Errorf("manifest entry %q has unknown kind %q", e.PathString(), e.Kind)
		}
		if i > 0 {
			switch c := bytes.Compare(m.Entries[i-1].RawPath(), e.RawPath()); {
			case c == 0:
				return fmt.Errorf("manifest lists %q twice", e.PathString())
			case c > 0:
				return fmt.Errorf("manifest is not sorted by path: %q before %q", m.Entries[i-1].PathString(), e.PathString())
			}
		}
	}
	return nil
}

// LoadManifest reads a stored manifest back by its hash, verifying it on the
// way. The result diffs exactly as the captured manifest did.
func LoadManifest(ctx context.Context, store *artifact.Store, hash string) (Manifest, error) {
	raw, err := store.ReadAll(ctx, hash, 256<<20)
	if err != nil {
		return Manifest{}, err
	}
	var doc manifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Manifest{}, fmt.Errorf("artifact %s is not a snapshot manifest: %w", hash, err)
	}
	if doc.Version != 1 {
		return Manifest{}, fmt.Errorf("artifact %s is manifest version %d; this reads 1", hash, doc.Version)
	}
	m := Manifest{Hash: hash, Complete: doc.Complete, Errors: doc.Errors, Entries: doc.Entries}
	if m.Errors == nil {
		m.Errors = []string{}
	}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
