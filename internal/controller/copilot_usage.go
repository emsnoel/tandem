package controller

// Usage from Copilot CLI's own logs.
//
// Copilot writes each conversation to <home>/session-state/<session>/
// events.jsonl, with every finished model call as a session.usage_record event.
// inputTokens counts every input token, cache included, and cacheReadTokens
// the reads, the same semantics as Codex.
//
// internal/usageledger has no Copilot parser, so a collaboration does not
// count Copilot sources; the run-scoped report does.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
)

const providerCopilot = "copilot"

// copilotSessionID is what a Copilot session id looks like. It names a
// directory, so nothing else is ever put in a path.
var copilotSessionID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]*$`)

// copilotMinMarker is the shortest text copilotSessionContaining searches for,
// as rollout.SessionContaining: a nonce is 32 hex digits.
const copilotMinMarker = 8

func copilotEvents(home, session string) string {
	return filepath.Join(home, "session-state", session, "events.jsonl")
}

// copilotStreams returns the session's one stream. Copilot writes a
// conversation only at its first turn, so before that there is no log and the
// stream is unavailable.
func copilotStreams(home, session string) []rollout.Stream {
	s := rollout.Stream{Provider: providerCopilot, SessionID: session}
	if !copilotSessionID.MatchString(session) {
		s.Unavailable = "the session id is not a Copilot session id, so no log was looked for"
		return []rollout.Stream{s}
	}
	s.File = copilotEvents(home, session)
	f, err := os.Open(s.File)
	if err != nil {
		s.Unavailable = "no Copilot log was found for this session"
		return []rollout.Stream{s}
	}
	defer f.Close()
	return []rollout.Stream{readCopilotStream(f, s)}
}

type copilotEvent struct {
	Type string `json:"type"`
	Data struct {
		Usage *struct {
			Model      string `json:"model"`
			Input      *int64 `json:"inputTokens"`
			Output     *int64 `json:"outputTokens"`
			CacheRead  *int64 `json:"cacheReadTokens"`
			APICallID  string `json:"apiCallId"`
			Accounting struct {
				UsageID string `json:"usageId"`
			} `json:"accounting"`
		} `json:"usage"`
	} `json:"data"`
}

// readCopilotStream reads one events.jsonl as rollout.ParseUsage reads a Codex
// log: a last line that does not decode is one still being written, and marks
// the stream truncated; any other is a log that cannot be trusted at all.
func readCopilotStream(f *os.File, s rollout.Stream) rollout.Stream {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var pending error
	n := 0
	for sc.Scan() {
		n++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		if pending != nil {
			s.Unavailable, s.Deltas, s.Rejected = pending.Error(), nil, 0
			return s
		}
		var e copilotEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			pending = fmt.Errorf("line %d does not decode: %v", n, err)
			continue
		}
		if e.Type != "session.usage_record" {
			continue
		}
		u := e.Data.Usage
		if u == nil || u.Input == nil || u.Output == nil || u.CacheRead == nil ||
			*u.Input < 0 || *u.Output < 0 || *u.CacheRead < 0 || *u.CacheRead > *u.Input {
			s.Rejected++
			s.Diagnostics = append(s.Diagnostics, fmt.Sprintf("line %d: a usage record with a count missing, negative, or cached input above input; not counted", n))
			continue
		}
		id := u.Accounting.UsageID
		if id == "" {
			id = u.APICallID
		}
		if id == "" {
			id = fmt.Sprintf("line:%d", n)
		}
		s.Deltas = append(s.Deltas, rollout.Delta{SourceID: copilotSourceID(s.SessionID, id), Line: n, Model: u.Model,
			Counters: rollout.Counters{Input: *u.Input, Cached: *u.CacheRead, Output: *u.Output}})
	}
	if err := sc.Err(); err != nil {
		s.Unavailable, s.Deltas, s.Rejected = "reading the log failed: "+err.Error(), nil, 0
		return s
	}
	if pending != nil {
		s.Truncated = true
		s.Diagnostics = append(s.Diagnostics, "the log ends mid-line, so later usage is not counted")
	}
	if len(s.Deltas) == 0 && s.Rejected == 0 {
		s.Unavailable = "the log has no usage records"
		s.NoUsageEvents = true
	}
	return s
}

// copilotSourceID names one model call by the id Copilot gave it, so reading
// the log again finds the same record and never adds to it.
func copilotSourceID(session, usageID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|usage:%s", providerCopilot, session, usageID)))
	return hex.EncodeToString(sum[:16])
}

// copilotSessionContaining returns the conversation whose prompt held marker,
// among those written from a day before from, or "" when none did. Two matching
// conversations are an error, never a guess.
func copilotSessionContaining(home, marker string, from, to time.Time) (string, error) {
	if len(marker) < copilotMinMarker {
		return "", fmt.Errorf("a marker of %d characters is too short to identify one conversation", len(marker))
	}
	if to.Before(from) {
		return "", fmt.Errorf("the interval ends before it starts")
	}
	logs, _ := filepath.Glob(filepath.Join(home, "session-state", "*", "events.jsonl"))
	sort.Strings(logs)
	since := from.AddDate(0, 0, -1)
	var found []string
	for _, p := range logs {
		id := filepath.Base(filepath.Dir(p))
		if !copilotSessionID.MatchString(id) || modTime(p).Before(since) || !copilotPromptHolds(p, marker) {
			continue
		}
		found = append(found, id)
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d conversations hold %s (%v), so the task's cannot be told", len(found), marker, found)
}

// copilotPromptHolds reports whether a user.message event in the log holds
// marker. The nonce quoted back in a reply is not the task arriving.
func copilotPromptHolds(path, marker string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, marker) {
			continue
		}
		var e struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Type == "user.message" {
			return true
		}
	}
	return false
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
