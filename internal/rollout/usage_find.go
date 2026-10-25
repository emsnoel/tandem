package rollout

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// minMarker is the shortest text SessionContaining searches for. A nonce is 32
// hex digits; anything much shorter could match logs it has nothing to do with.
const minMarker = 8

// SessionContaining returns the id of the root conversation whose log, filed on
// the days around from..to as for Descendants, contains marker, or "" when none
// does.
//
// It finds a task's conversation when nobody reported it: Herdr learns a Codex
// conversation only from a hook that may not run. The task's prompt carries its
// nonce and Codex logs every prompt. A child conversation handed the prompt is
// not the task's, and two root logs with one nonce are an error, never a guess.
func SessionContaining(sessionsDir, marker string, from, to time.Time) (string, error) {
	if len(marker) < minMarker {
		return "", fmt.Errorf("a marker of %d characters is too short to identify one conversation", len(marker))
	}
	if to.Before(from) {
		return "", fmt.Errorf("the interval ends before it starts")
	}
	first, last := from.AddDate(0, 0, -1), to.AddDate(0, 0, 1)
	if last.Sub(first) > maxIntervalDays*24*time.Hour {
		return "", fmt.Errorf("the interval is longer than %d days", maxIntervalDays)
	}
	var found []string
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		matches, _ := filepath.Glob(filepath.Join(sessionsDir, d.Format("2006/01/02"), "rollout-*.jsonl"))
		sort.Strings(matches)
		for _, p := range matches {
			id, parent := readMeta(p)
			if id == "" || parent != "" || !fileContains(p, []byte(marker)) {
				continue
			}
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d conversations hold %s (%v), so the task's cannot be told", len(found), marker, found)
}

// fileContains reports whether the file holds b. It reads in chunks and keeps
// the last len(b)-1 bytes of each, so a match across two chunks is found.
func fileContains(path string, b []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	keep := len(b) - 1
	buf := make([]byte, keep+1<<16)
	carried := 0
	for {
		n, err := f.Read(buf[carried:])
		window := buf[:carried+n]
		if bytes.Contains(window, b) {
			return true
		}
		if err == io.EOF || (err != nil && n == 0) {
			return false
		}
		carried = copy(buf, window[max(0, len(window)-keep):])
	}
}
