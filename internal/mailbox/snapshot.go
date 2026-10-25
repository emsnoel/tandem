// Package mailbox reads the relay mailbox without making policy decisions.
package mailbox

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot maps report ids to their modification time in nanoseconds.
type Snapshot map[string]int64

// Take records every Markdown handback report in reportsDir.
func Take(reportsDir string) (Snapshot, error) {
	entries, err := os.ReadDir(reportsDir)
	// A missing directory means no reports yet.
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot := Snapshot{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		snapshot[id] = info.ModTime().UnixNano()
	}
	return snapshot, nil
}

// Diff returns sorted ids that were added, removed or rewritten in other.
func (s Snapshot) Diff(other Snapshot) []string {
	changed := make(map[string]bool)
	for id, before := range s {
		if after, ok := other[id]; !ok || after != before {
			changed[id] = true
		}
	}
	// Also catch ids only in other.
	for id, after := range other {
		if before, ok := s[id]; !ok || before != after {
			changed[id] = true
		}
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
