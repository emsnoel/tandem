// Package sourcehash computes the fingerprint install.sh stamps into the tandem
// binary and the staleness test checks. It is in Go so the fingerprint is
// NUL-safe and locale-independent, and imports only the standard library.
package sourcehash

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// Inputs: every .go, .sql and .tmpl file under cmd/ and internal/, plus go.mod
// and, when present, go.sum. Tests are inputs too: they are part of what the
// suite verifies. Databases, caches and documents are not.
var (
	trees      = []string{"cmd", "internal"}
	extensions = map[string]bool{".go": true, ".sql": true, ".tmpl": true}
)

// Hash returns the hex sha256 of the build inputs under root. For each input,
// in bytewise order of its slash-separated relative path, it hashes the path
// length as a big-endian uint64, the path, the content length likewise, and the
// content. The length prefixes make boundaries unambiguous, so no pair of
// different trees can share a byte stream. The checkout's own location is
// never part of the input.
//
// It fails closed: a root without go.mod, a tree with no inputs, or a symlink
// where an input should be is an error, never a hash of less than was meant.
func Hash(root string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("%s is not a Go module root: %w", root, err)
	}
	var rels []string
	for _, tree := range trees {
		dir := filepath.Join(root, tree)
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !extensions[path.Ext(d.Name())] {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink; build inputs must be regular files", p)
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", p)
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rels = append(rels, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	rels = append(rels, "go.mod")
	if _, err := os.Lstat(filepath.Join(root, "go.sum")); err == nil {
		rels = append(rels, "go.sum")
	}
	if len(rels) < 2 {
		return "", fmt.Errorf("no build inputs under %s", root)
	}
	sort.Strings(rels) // bytewise, whatever the locale

	h := sha256.New()
	var n [8]byte
	for _, rel := range rels {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if info, err := os.Lstat(full); err != nil {
			return "", err
		} else if info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symlink; build inputs must be regular files", rel)
		}
		body, err := os.ReadFile(full)
		if err != nil {
			return "", err
		}
		binary.BigEndian.PutUint64(n[:], uint64(len(rel)))
		h.Write(n[:])
		h.Write([]byte(rel))
		binary.BigEndian.PutUint64(n[:], uint64(len(body)))
		h.Write(n[:])
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
