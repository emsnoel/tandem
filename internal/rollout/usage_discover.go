package rollout

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Found is a session discovered by ancestry.
type Found struct {
	Path, SessionID, ParentID string
}

// FindSession returns the rollout log for a session id under sessionsDir
// (YYYY/MM/DD/rollout-<time>-<id>.jsonl), or "" when there is none.
func FindSession(sessionsDir, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return "", nil
	}
	m, err := filepath.Glob(filepath.Join(sessionsDir, "*", "*", "*", "rollout-*-"+id+".jsonl"))
	if err != nil || len(m) == 0 {
		return "", err
	}
	sort.Strings(m)
	return m[len(m)-1], nil
}

// maxIntervalDays bounds a search; a run is not longer than this.
const maxIntervalDays = 400

// Descendants returns the sessions descended from rootID, at any depth, whose
// logs are filed on any day from the day before from to the day after to (a
// session may be filed either side of midnight from the run it served). Each
// session is returned once, parents before children, whatever the logs claim:
// a visited set stops a cycle, and a second log for one session id is ignored.
func Descendants(sessionsDir, rootID string, from, to time.Time) ([]Found, error) {
	if to.Before(from) {
		return nil, fmt.Errorf("the interval ends before it starts")
	}
	first, last := from.AddDate(0, 0, -1), to.AddDate(0, 0, 1)
	if last.Sub(first) > maxIntervalDays*24*time.Hour {
		return nil, fmt.Errorf("the interval is longer than %d days", maxIntervalDays)
	}
	children := map[string][]Found{}
	seenFile := map[string]bool{}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		matches, _ := filepath.Glob(filepath.Join(sessionsDir, d.Format("2006/01/02"), "rollout-*.jsonl"))
		sort.Strings(matches)
		for _, p := range matches {
			if seenFile[p] {
				continue
			}
			seenFile[p] = true
			if id, parent := readMeta(p); id != "" && parent != "" {
				children[parent] = append(children[parent], Found{Path: p, SessionID: id, ParentID: parent})
			}
		}
	}
	visited := map[string]bool{rootID: true}
	var out []Found
	for level := []string{rootID}; len(level) > 0; {
		var next []string
		for _, parent := range level {
			for _, f := range children[parent] {
				if visited[f.SessionID] {
					continue
				}
				visited[f.SessionID] = true
				out = append(out, f)
				next = append(next, f.SessionID)
			}
		}
		level = next
	}
	return out, nil
}

// readMeta reads a rollout's session id and parent from its first line.
func readMeta(path string) (id, parent string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 1<<16).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return "", ""
	}
	var m usageEvent
	if json.Unmarshal(line, &m) != nil || m.Type != "session_meta" {
		return "", ""
	}
	return m.Payload.ID, m.Payload.Parent
}

// Collection is every stream found for one root session.
type Collection struct {
	Streams []Stream
}

// Collect reads the root session's log and the logs of everything descended from
// it over the interval. A root with no log is one unavailable stream, so the gap
// is visible rather than an empty result that reads as zero.
func Collect(sessionsDir, rootID string, from, to time.Time) Collection {
	var c Collection
	root, _ := FindSession(sessionsDir, rootID)
	if root == "" {
		c.Streams = append(c.Streams, Stream{Provider: providerCodex, SessionID: rootID, Unavailable: "no rollout log was found for this session"})
	} else {
		c.Streams = append(c.Streams, ReadStream(root))
	}
	found, err := Descendants(sessionsDir, rootID, from, to)
	if err != nil {
		c.Streams[0].Diagnostics = append(c.Streams[0].Diagnostics, "descendants were not searched: "+err.Error())
	}
	for _, f := range found {
		s := ReadStream(f.Path)
		if s.SessionID == "" {
			s.SessionID, s.ParentID = f.SessionID, f.ParentID
		}
		c.Streams = append(c.Streams, s)
	}
	return c
}

// Aggregate adds the streams' usage into one figure and says what is missing.
// A stream whose log is unavailable adds nothing and makes the total partial; a
// truncated one or one with rejected events adds what it has and does the same.
// A stream marked IncludesChildren already counts its descendants, which are
// then skipped.
func Aggregate(streams []Stream) (workflow.Usage, []string) {
	covered := Covered(streams)
	var parts []workflow.Usage
	var notes []string
	for _, s := range streams {
		name := s.SessionID
		if name == "" {
			name = filepath.Base(s.File)
		}
		if by, skip := covered[s.SessionID]; skip && s.SessionID != "" {
			notes = append(notes, fmt.Sprintf("session %s: included in the totals of its parent %s, not counted again", name, by))
			continue
		}
		if s.Unavailable != "" {
			parts = append(parts, workflow.Usage{Completeness: workflow.UsageUnavailable, InputSemantics: workflow.SemanticsUnknown})
			notes = append(notes, fmt.Sprintf("session %s: %s", name, s.Unavailable))
			continue
		}
		var in, cached, out int64
		for _, d := range s.Deltas {
			in, cached, out = in+d.Counters.Input, cached+d.Counters.Cached, out+d.Counters.Output
		}
		u := workflow.Usage{Input: workflow.Int64(in), CachedInput: workflow.Int64(cached), Output: workflow.Int64(out),
			Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}
		if s.Truncated || s.Rejected > 0 {
			u.Completeness = workflow.UsagePartial
		}
		for _, d := range s.Diagnostics {
			notes = append(notes, fmt.Sprintf("session %s: %s", name, d))
		}
		parts = append(parts, u)
	}
	return workflow.SumUsage(parts), notes
}

// Covered returns, for each session whose usage an ancestor already counts, that
// ancestor: the sessions below a stream marked IncludesChildren.
func Covered(streams []Stream) map[string]string {
	parentOf := map[string]string{}
	includes := map[string]bool{}
	for _, s := range streams {
		if s.SessionID != "" {
			parentOf[s.SessionID] = s.ParentID
			includes[s.SessionID] = s.IncludesChildren
		}
	}
	out := map[string]string{}
	for _, s := range streams {
		seen := map[string]bool{}
		for p := s.ParentID; p != "" && !seen[p]; p = parentOf[p] {
			seen[p] = true
			if includes[p] {
				out[s.SessionID] = p
				break
			}
		}
	}
	return out
}

// FromPhase converts a legacy phase into a Usage. A phase that saw no calls says
// nothing about usage, so it is unavailable rather than zero.
func FromPhase(p Phase) workflow.Usage {
	if p.Calls == 0 && p.Input == 0 && p.Cached == 0 && p.Output == 0 {
		return workflow.Usage{Completeness: workflow.UsageUnavailable, InputSemantics: workflow.SemanticsUnknown}
	}
	return workflow.Usage{Input: workflow.Int64(p.Input), CachedInput: workflow.Int64(p.Cached), Output: workflow.Int64(p.Output),
		Completeness: workflow.UsageComplete, InputSemantics: workflow.SemanticsIncludesCache}
}
