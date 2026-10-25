package build

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// HistoryDir is where archived builds live, under the repository root and
// outside either relay spelling, so deleting a relay never deletes history.
const HistoryDir = ".tandem-history"

// ErrEscapes is returned when a build record points at evidence outside the
// relay it came from, or at a symlink. Such a path is never copied.
var ErrEscapes = errors.New("evidence outside the build's relay")

// writeArchiveFile writes one archived file. Tests replace it to inject a
// short write; VerifyArchive must then catch it.
var writeArchiveFile = func(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

// Manifest lists every archived file with the hash of its exact bytes.
type Manifest struct {
	BuildID string         `json:"build_id"`
	Source  string         `json:"source"`
	Files   []ManifestFile `json:"files"`
	// Missing names referenced files that were already gone when archiving ran.
	Missing []string `json:"missing,omitempty"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ArchiveResult says where a build went. Dir is empty when there was no build.
type ArchiveResult struct {
	Dir      string `json:"archive"`
	Manifest string `json:"manifest,omitempty"`
	Files    int    `json:"files"`
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Archive copies the build in dir and the evidence it references into
// <root>/.tandem-history/legacy/<id>-<suffix>/, rewrites the archived record to
// point at the copies, writes a sorted manifest and verifies every byte. Only
// then does it return; the source is never touched, so any failure leaves it
// as it was and removes the partial archive.
//
// Evidence must live in the build directory or elsewhere under its relay. A
// path outside those, or a symlink, is ErrEscapes: the record is builder
// written and is not trusted to name arbitrary files to copy.
func Archive(root, dir string) (ArchiveResult, error) {
	b, err := Load(dir)
	if err != nil {
		return ArchiveResult{}, err
	}
	if b == nil {
		return ArchiveResult{}, nil
	}
	relay := filepath.Dir(dir)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ArchiveResult{}, err
	}
	realRelay, err := filepath.EvalSymlinks(relay)
	if err != nil {
		return ArchiveResult{}, err
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return ArchiveResult{}, err
	}
	id := unsafeID.ReplaceAllString(b.ID, "_")
	if id == "" {
		id = "build"
	}
	history := filepath.Join(root, HistoryDir)
	dest := filepath.Join(history, "legacy", id+"-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(filepath.Join(dest, "build"), 0o755); err != nil {
		return ArchiveResult{}, err
	}
	// The directory ignores itself, so archived evidence never reaches
	// `git add -A` and no user-owned .gitignore is edited.
	if err := os.WriteFile(filepath.Join(history, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		os.RemoveAll(dest)
		return ArchiveResult{}, err
	}

	res, err := copyBuild(b, dir, realDir, realRelay, dest)
	if err != nil {
		os.RemoveAll(dest)
		return ArchiveResult{}, err
	}
	if err := VerifyArchive(dest); err != nil {
		os.RemoveAll(dest)
		return ArchiveResult{}, err
	}
	res.Dir, res.Manifest = dest, filepath.Join(dest, "manifest.json")
	return res, nil
}

func copyBuild(b *Build, dir, realDir, realRelay, dest string) (ArchiveResult, error) {
	m := Manifest{BuildID: b.ID, Source: dir}
	add := func(rel string, data []byte) error {
		path := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeArchiveFile(path, data); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m.Files = append(m.Files, ManifestFile{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
		return nil
	}

	// The build directory itself, except the record, which is rewritten below.
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular():
			return fmt.Errorf("%w: %s is not a regular file", ErrEscapes, path)
		case rel == recordName || strings.HasSuffix(rel, ".tmp"):
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return add(filepath.Join("build", rel), data)
	})
	if err != nil {
		return ArchiveResult{}, err
	}

	// Referenced files. Those already inside the build directory were copied
	// above; others under the relay are copied to referenced/.
	next := 0
	rewrite := func(ref *string) error {
		if *ref == "" {
			return nil
		}
		real, err := evidencePath(*ref, realRelay)
		if errors.Is(err, fs.ErrNotExist) {
			m.Missing = append(m.Missing, *ref)
			return nil
		}
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(realDir, real); err == nil && !strings.HasPrefix(rel, "..") {
			*ref = filepath.Join(dest, "build", rel)
			return nil
		}
		data, err := os.ReadFile(real)
		if err != nil {
			return err
		}
		next++
		rel := filepath.Join("referenced", fmt.Sprintf("%d-%s", next, filepath.Base(real)))
		if err := add(rel, data); err != nil {
			return err
		}
		*ref = filepath.Join(dest, rel)
		return nil
	}
	out := *b
	out.Tasks = append([]Task(nil), b.Tasks...)
	for i := range out.Tasks {
		t := &out.Tasks[i]
		for _, ref := range []*string{&t.Brief, &t.CheckLog, &t.Report, &t.ReviewReport} {
			if err := rewrite(ref); err != nil {
				return ArchiveResult{}, err
			}
		}
	}
	record, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return ArchiveResult{}, err
	}
	if err := add(filepath.Join("build", recordName), append(record, '\n')); err != nil {
		return ArchiveResult{}, err
	}

	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	sort.Strings(m.Missing)
	raw, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return ArchiveResult{}, err
	}
	// The manifest describes the other files, so it is not one of them.
	if err := writeArchiveFile(filepath.Join(dest, "manifest.json"), append(raw, '\n')); err != nil {
		return ArchiveResult{}, err
	}
	return ArchiveResult{Files: len(m.Files)}, nil
}

// evidencePath resolves a path a build record names. It must be a regular file,
// not a symlink, under realRelay once its directories are resolved.
func evidencePath(path, realRelay string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is a symlink or not a regular file", ErrEscapes, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(realRelay, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrEscapes, path)
	}
	return real, nil
}

// VerifyArchive re-reads every file the manifest lists and checks its hash and
// size, so a short or corrupted write is an error and not silent history loss.
func VerifyArchive(archive string) error {
	raw, err := os.ReadFile(filepath.Join(archive, "manifest.json"))
	if err != nil {
		return err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if len(m.Files) == 0 {
		return errors.New("archive manifest lists no files")
	}
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(archive, filepath.FromSlash(f.Path)))
		if err != nil {
			return fmt.Errorf("archived %s: %w", f.Path, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 || int64(len(data)) != f.Size {
			return fmt.Errorf("archived %s does not match its manifest (wrote %d bytes, want %d)", f.Path, len(data), f.Size)
		}
	}
	return nil
}
