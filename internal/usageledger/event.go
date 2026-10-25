// Package usageledger normalizes provider usage logs into per-event
// contributions for the collaboration ledger.
//
// The external token eval keeps a copy of the golden fixtures in
// testdata/usage, and shares nothing else, so each checks the other. Unknown
// counters stay nil and every gap adds a reason. internal/rollout's builder
// report is separate and narrower: Codex input, cached and output for builder
// sessions only.
package usageledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ErrUnknownProvider is returned for a provider this package cannot parse.
var ErrUnknownProvider = errors.New("unknown usage provider")

// Event is one provider event's normalized contribution. Counters are nil when
// unknown. ReasoningOutput is a subset of Output, never added to it.
type Event struct {
	Provider         string         `json:"provider"`
	Model            *string        `json:"model"`
	SessionID        *string        `json:"session_id"`
	EventID          string         `json:"event_id"`
	Revision         int64          `json:"revision"`
	InputTotal       *int64         `json:"input_total"`
	CacheRead        *int64         `json:"cache_read"`
	CacheWrite       *int64         `json:"cache_write"`
	Output           *int64         `json:"output"`
	ReasoningOutput  *int64         `json:"reasoning_output"`
	SourceCursor     int            `json:"source_cursor"`
	SourceHash       string         `json:"source_hash"`
	TerminalEvidence bool           `json:"terminal_evidence"`
	Components       map[string]any `json:"components"`
}

// Batch is everything one source's bytes say about usage: the latest
// revision of each event, the revisions they replaced, and why the source is
// incomplete if it is.
type Batch struct {
	Provider   string   `json:"provider"`
	Events     []Event  `json:"events"`
	Superseded []Event  `json:"superseded"`
	Complete   bool     `json:"complete"`
	Reasons    []string `json:"reasons"`
}

// record is one decoded JSONL line. cursor is its zero-based line number.
type record struct {
	cursor int
	hash   string
	raw    map[string]any
}

type reasons struct{ list []string }

func (r *reasons) add(format string, args ...any) {
	r.list = append(r.list, fmt.Sprintf(format, args...))
}

// Normalize parses one source's complete, bounded bytes: the incremental
// parser run over all of them, with the last line read as a final one. It is
// the batch form for golden fixtures; collection uses NormalizeIncremental.
func Normalize(provider string, raw []byte) (Batch, error) {
	c := Cursor{Provider: provider}
	p, err := c.parser()
	if err != nil {
		return Batch{}, err
	}
	var events, superseded []Event
	index := map[string]int{}
	emit := func(e Event) {
		if i, ok := index[e.EventID]; ok {
			superseded = append(superseded, events[i])
			events[i] = e
			return
		}
		index[e.EventID] = len(events)
		events = append(events, e)
	}
	rs := &reasons{}
	n := c.consume(p, raw, rs, emit)
	c.rest(p, raw[n:], true, rs, emit)
	sort.SliceStable(superseded, func(i, j int) bool { return superseded[i].SourceCursor < superseded[j].SourceCursor })
	b := Batch{Provider: provider, Events: c.Settle(events), Superseded: superseded, Reasons: closeReasons(p, rs)}
	if b.Events == nil {
		b.Events = []Event{}
	}
	if b.Superseded == nil {
		b.Superseded = []Event{}
	}
	b.Complete = len(b.Reasons) == 0
	return b, nil
}

func decodeOne(line []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

var integer = regexp.MustCompile(`^-?[0-9]+$`)

// counter is a valid token counter: a JSON integer from 0 to MaxInt64. A
// fraction, exponent, bool, string or overflow is not.
func counter(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || !integer.MatchString(n.String()) {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return i, err == nil && i >= 0
}

// add returns a+b, or false when the sum overflows int64.
func add(a, b int64) (int64, bool) {
	if b > 0 && a > (1<<63-1)-b {
		return 0, false
	}
	return a + b, true
}

func ptr(v int64) *int64 { return &v }

func text(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// canonical is sorted-key compact JSON, used to compare repeated records.
func canonical(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("unencodable %T", v)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func newEvent(provider string, model, session *string, id string, revision int64, r record, components map[string]any) Event {
	return Event{Provider: provider, Model: model, SessionID: session, EventID: id, Revision: revision,
		SourceCursor: r.cursor, SourceHash: r.hash, Components: components}
}

func hasPrefix(list []string, prefixes ...string) bool {
	for _, r := range list {
		for _, p := range prefixes {
			if strings.HasPrefix(r, p) {
				return true
			}
		}
	}
	return false
}

func dedupe(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, r := range list {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}
