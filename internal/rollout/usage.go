package rollout

// Usage from Codex rollout logs, with known and unknown kept apart.
//
// Phase (rollout.go) answers where a session's tokens went. This file answers
// how much a run used: every session it started, at any depth and on any day,
// each counted once, with a visible gap wherever a log is missing, unreadable
// or cut off.
//
//   - Cumulative counters are differenced within one log only. A counter that
//     goes backwards starts a new segment, and a repeated event adds nothing.
//   - Every event's source id binds provider, session, log file and line, so
//     rereading a log names the same events the same way.
//   - A log that cannot be trusted contributes null, not zero.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Counters are token counts as Codex reports them: Input includes the cached
// part.
type Counters struct {
	Input, Cached, Output int64
}

// Deltas turns a log's cumulative totals into the usage of each event. The first
// event's delta is its own totals. After that a delta is the difference from the
// previous event, so a repeat is zero; if any counter has gone down the counter
// restarted, and the event's totals are the new segment's first delta.
func Deltas(totals []Counters) []Counters {
	out := make([]Counters, 0, len(totals))
	var prev Counters
	for i, c := range totals {
		if i == 0 || c.Input < prev.Input || c.Cached < prev.Cached || c.Output < prev.Output {
			out = append(out, c)
		} else {
			out = append(out, Counters{c.Input - prev.Input, c.Cached - prev.Cached, c.Output - prev.Output})
		}
		prev = c
	}
	return out
}

// Delta is the usage of one logged event.
type Delta struct {
	SourceID string
	Line     int
	Model    string
	Counters Counters
}

// Stream is everything one rollout log says about usage.
type Stream struct {
	Provider  string
	SessionID string
	ParentID  string
	File      string
	Deltas    []Delta
	// IncludesChildren is set by a caller that knows this log's totals already
	// count the sessions it spawned, so those are not counted again.
	IncludesChildren bool
	// Truncated: the log ends mid-line, as one still being written does. What it
	// holds is a lower bound.
	Truncated bool
	// Rejected counts events that could not be true and were left out.
	Rejected int
	// Unavailable is why the log gave nothing usable; Deltas is empty.
	Unavailable string
	// NoUsageEvents: the log was found and read but holds no usage events.
	// For accounting that is unknown usage (Unavailable is set too); for a
	// turn that ended at once it means the model did no work.
	NoUsageEvents bool
	Diagnostics   []string
}

type usageEvent struct {
	Type    string `json:"type"`
	Payload struct {
		ID     string `json:"id"`
		Parent string `json:"parent_thread_id"`
		Type   string `json:"type"`
		Model  string `json:"model"`
		Info   *struct {
			Total *tokenCounts `json:"total_token_usage"`
			Last  *tokenCounts `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type tokenCounts struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Output int64 `json:"output_tokens"`
}

const providerCodex = "codex"

// ParseUsage reads one rollout log. A last line that does not decode is a log
// still being written, and marks the stream truncated; any other line that does
// not decode makes the whole log untrustworthy and the stream unavailable.
func ParseUsage(r io.Reader, file string) Stream {
	s := Stream{Provider: providerCodex, File: file}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	type sample struct {
		line  int
		model string
		c     Counters
		cum   bool
	}
	var samples []sample
	model := ""
	var pending error
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		if pending != nil {
			s.Unavailable = pending.Error()
			s.Deltas, s.Truncated = nil, false
			return s
		}
		var e usageEvent
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			pending = fmt.Errorf("line %d does not decode: %v", n, err)
			continue
		}
		switch {
		case e.Type == "session_meta" && s.SessionID == "":
			s.SessionID, s.ParentID = e.Payload.ID, e.Payload.Parent
		case e.Type == "turn_context" && e.Payload.Model != "":
			model = e.Payload.Model
		case e.Type == "event_msg" && e.Payload.Type == "token_count" && e.Payload.Info != nil:
			switch {
			case e.Payload.Info.Total != nil:
				t := e.Payload.Info.Total
				samples = append(samples, sample{n, model, Counters{t.Input, t.Cached, t.Output}, true})
			case e.Payload.Info.Last != nil:
				l := e.Payload.Info.Last
				samples = append(samples, sample{n, model, Counters{l.Input, l.Cached, l.Output}, false})
			}
		}
	}
	if err := sc.Err(); err != nil {
		s.Unavailable = "reading the log failed: " + err.Error()
		s.Deltas = nil
		return s
	}
	s.Truncated = pending != nil
	if pending != nil {
		s.Diagnostics = append(s.Diagnostics, "the log ends mid-line, so later usage is not counted")
	}

	// Cumulative samples are differenced across the ones that are valid; an
	// impossible one is left out and does not move the baseline.
	var prev *Counters
	for _, sm := range samples {
		if sm.c.Input < 0 || sm.c.Cached < 0 || sm.c.Output < 0 || sm.c.Cached > sm.c.Input {
			s.Rejected++
			s.Diagnostics = append(s.Diagnostics, fmt.Sprintf("line %d: cached input %d is more than input %d, or a count is negative; the event is not counted", sm.line, sm.c.Cached, sm.c.Input))
			continue
		}
		d := sm.c
		if sm.cum {
			if prev != nil {
				d = Deltas([]Counters{*prev, sm.c})[1]
			}
			c := sm.c
			prev = &c
		}
		s.Deltas = append(s.Deltas, Delta{SourceID: sourceID(s.Provider, s.SessionID, file, sm.line), Line: sm.line, Model: sm.model, Counters: d})
	}
	if len(s.Deltas) == 0 && s.Rejected == 0 {
		s.Unavailable = "the log has no token_count events"
		s.NoUsageEvents = true
	}
	return s
}

// sourceID names one logged event: the same event always has the same id, and no
// two events share one. The file's base name is used, not its directory, so a log
// that is moved or copied keeps its ids.
func sourceID(provider, session, file string, line int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", provider, session, filepath.Base(file), line)))
	return hex.EncodeToString(sum[:16])
}

// ReadStream opens and parses a rollout log. A log that cannot be opened is an
// unavailable stream, not an error: the caller reports the gap.
func ReadStream(path string) Stream {
	f, err := os.Open(path)
	if err != nil {
		return Stream{Provider: providerCodex, File: path, Unavailable: "cannot be opened: " + err.Error()}
	}
	defer f.Close()
	s := ParseUsage(f, path)
	if s.SessionID == "" {
		if id, parent := readMeta(path); id != "" {
			s.SessionID, s.ParentID = id, parent
		}
	}
	return s
}
