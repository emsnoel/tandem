package vcs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Deleted is the hash DirtyHashes records for a path git lists but that is no
// longer on disk.
const Deleted = "deleted"

// readFile is os.ReadFile; tests replace it to inject a read failure.
var readFile = os.ReadFile

// DirtyHashes maps every path git reports as changed or untracked to a hash of
// what is on disk, or Deleted. Snapshots before and after a task show which
// files it changed without commits between tasks.
//
// The hash covers the entry kind, executable bit and content (or a symlink's
// target text, never followed). Only a missing path is Deleted; any other read
// failure is an error.
func DirtyHashes(root string) (map[string]string, error) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil, err
	}
	h := map[string]string{}
	entries := bytes.Split(out, []byte{0})
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		path := string(e[3:])
		// A rename or copy is followed by its source path as its own entry.
		if e[0] == 'R' || e[0] == 'C' {
			i++
		}
		sum, err := hashEntry(filepath.Join(root, path))
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", path, err)
		}
		h[path] = sum
	}
	return h, nil
}

// hashEntry hashes one path without following a symlink.
func hashEntry(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Deleted, nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	switch mode := info.Mode(); {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(sum, "symlink\x00%s", target)
	case mode.IsRegular():
		b, err := readFile(path)
		if os.IsNotExist(err) {
			return Deleted, nil
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(sum, "file\x00%t\x00", mode&0o111 != 0)
		sum.Write(b)
	case mode.IsDir():
		// A nested repository or submodule: git lists the directory itself.
		fmt.Fprint(sum, "dir\x00")
	default:
		fmt.Fprintf(sum, "other\x00%s", mode.Type())
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// Changed lists, sorted, the paths whose state differs between two
// DirtyHashes snapshots. A path absent from a snapshot was clean then.
func Changed(before, after map[string]string) []string {
	var out []string
	for p, a := range after {
		if before[p] != a {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
