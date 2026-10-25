package snapshot

import (
	"encoding/json"
	"sort"
)

// Children returns the artifacts a stored document names: the content of every
// file of a snapshot manifest, or of every change of a patch. Any other bytes
// name nothing. A collector or an export must keep or copy these with the
// document, because no database row refers to them.
func Children(raw []byte) []string {
	var probe struct {
		Version int      `json:"version"`
		Entries []Entry  `json:"entries"`
		Changes []Change `json:"changes"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Version != 1 {
		return nil
	}
	seen := map[string]bool{}
	for _, e := range probe.Entries {
		if e.ContentHash != "" {
			seen[e.ContentHash] = true
		}
	}
	for _, c := range probe.Changes {
		for _, h := range []string{c.BeforeHash, c.AfterHash} {
			if h != "" {
				seen[h] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
