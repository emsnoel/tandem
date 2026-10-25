package usageledger

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Incremental parsing. A Cursor is everything needed to go on parsing one
// source where the last call stopped, without reading what was already read:
// the byte offset and line number consumed so far, a running hash of those
// bytes, the provider parser's state and the reasons found so far. It encodes
// to JSON, so it can be committed with the event revisions it produced; a
// replay from a committed cursor is idempotent.
//
// A line is consumed only when it ends with a newline. An undecodable line is
// held back while nothing but blank lines follows it, because a whole-source
// read would call it a truncated tail, not malformed. What is held is read
// again on the next call, so a record split across chunks is never misread.

// Cursor is a provider parser's position and state in one source.
type Cursor struct {
	Provider   string       `json:"provider"`
	Offset     int64        `json:"offset"`
	Line       int          `json:"line"`
	HashState  []byte       `json:"hash_state,omitempty"`
	PrefixHash string       `json:"prefix_hash"`
	Reasons    []string     `json:"reasons"`
	Codex      *codexState  `json:"codex,omitempty"`
	Claude     *claudeState `json:"claude,omitempty"`
}

// parser is one provider's state machine. feed handles one decoded record;
// finish adds the reasons that depend on the whole source so far.
type parser interface {
	feed(r record, rs *reasons, emit func(Event))
	finish(rs *reasons)
	events() int
}

func (c *Cursor) parser() (parser, error) {
	switch c.Provider {
	case "codex":
		if c.Codex == nil {
			c.Codex = newCodexState()
		}
		return c.Codex, nil
	case "claude":
		if c.Claude == nil {
			c.Claude = newClaudeState()
		}
		return c.Claude, nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownProvider, c.Provider)
}

// clone deep-copies a cursor through its JSON form, so a view never changes
// the cursor it was taken from.
func (c Cursor) clone() Cursor {
	raw, err := json.Marshal(c)
	if err != nil {
		panic("usageledger: cursor does not encode: " + err.Error())
	}
	var out Cursor
	if err := json.Unmarshal(raw, &out); err != nil {
		panic("usageledger: cursor does not decode: " + err.Error())
	}
	return out
}

func blank(b []byte) bool { return len(bytes.TrimSpace(b)) == 0 }

// decodeObject decodes one line as a JSON object with exact numbers.
func decodeObject(line []byte) (map[string]any, error) {
	v, err := decodeOne(line)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

// consume parses the newline-terminated lines of data, which starts at the
// cursor's offset, and returns how many bytes it consumed.
func (c *Cursor) consume(p parser, data []byte, rs *reasons, emit func(Event)) int {
	pos := 0
	for {
		nl := bytes.IndexByte(data[pos:], '\n')
		if nl < 0 {
			return pos
		}
		line, next := data[pos:pos+nl], pos+nl+1
		if !blank(line) {
			obj, err := decodeObject(line)
			switch {
			case err != nil && blank(data[next:]):
				return pos // perhaps a tail: decide when more arrives
			case err != nil:
				rs.add("malformed_record:%d", c.Line)
			default:
				sum := sha256.Sum256(line)
				p.feed(record{cursor: c.Line, hash: hex.EncodeToString(sum[:]), raw: obj}, rs, emit)
			}
		}
		c.Line++
		pos = next
	}
}

// rest handles what consume held back. At the end of a whole source (final),
// each remaining line is read as a batch read would: an undecodable last line
// is a truncated tail. Otherwise the first held line is a pending tail.
func (c *Cursor) rest(p parser, held []byte, final bool, rs *reasons, emit func(Event)) {
	lines := bytes.Split(held, []byte("\n"))
	last := -1
	for i, l := range lines {
		if !blank(l) {
			last = i
		}
	}
	for i, l := range lines {
		if blank(l) {
			continue
		}
		n := c.Line + i
		if !final {
			rs.add("truncated_tail:%d", n)
			return
		}
		obj, err := decodeObject(l)
		switch {
		case err != nil && i == last:
			rs.add("truncated_tail:%d", n)
		case err != nil:
			rs.add("malformed_record:%d", n)
		default:
			sum := sha256.Sum256(l)
			p.feed(record{cursor: n, hash: hex.EncodeToString(sum[:]), raw: obj}, rs, emit)
		}
	}
}

func (c *Cursor) hashConsumed(b []byte) {
	h := sha256.New()
	if len(c.HashState) > 0 {
		if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(c.HashState); err != nil {
			panic("usageledger: corrupt hash state: " + err.Error())
		}
	}
	h.Write(b)
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		panic("usageledger: hash state does not encode: " + err.Error())
	}
	c.HashState, c.PrefixHash = state, hex.EncodeToString(h.Sum(nil))
}

// closeReasons adds the reasons that depend on the whole source so far.
func closeReasons(p parser, rs *reasons) []string {
	p.finish(rs)
	if p.events() == 0 && !hasPrefix(rs.list, "missing_event_id", "malformed_record", "truncated_tail") {
		rs.add("no_usage_events")
	}
	return dedupe(rs.list)
}

// view returns the reasons the source is incomplete so far, with held bytes
// read as a pending tail, without changing c.
func (c Cursor) view(held []byte) []string {
	v := c.clone()
	p, _ := v.parser()
	rs := &reasons{list: v.Reasons}
	v.rest(p, held, false, rs, nil)
	return closeReasons(p, rs)
}

// NormalizeIncremental parses raw, the source's bytes from cursor.Offset on,
// and returns the events found or revised by the lines it consumed, the
// source's reasons as they stand, and the cursor to commit with those events.
// An empty cursor names its provider and starts at the beginning.
func NormalizeIncremental(provider string, raw []byte, cursor Cursor) (Batch, Cursor, error) {
	if cursor.Provider == "" {
		cursor.Provider = provider
	}
	if cursor.Provider != provider {
		return Batch{}, cursor, fmt.Errorf("a %s cursor cannot continue a %s source", cursor.Provider, provider)
	}
	next := cursor.clone()
	p, err := next.parser()
	if err != nil {
		return Batch{}, cursor, err
	}
	rs := &reasons{list: next.Reasons}
	emitted := []Event{}
	n := next.consume(p, raw, rs, func(e Event) { emitted = append(emitted, e) })
	next.Reasons = rs.list
	next.Offset += int64(n)
	next.hashConsumed(raw[:n])
	for i := range emitted {
		next.settle(&emitted[i])
	}
	reasons := next.view(raw[n:])
	return Batch{Provider: provider, Events: emitted, Superseded: []Event{}, Reasons: reasons, Complete: len(reasons) == 0}, next, nil
}

// settle sets an event's terminal evidence from what the cursor has seen. A
// Codex event becomes terminal when a later task_complete arrives, so events
// stored earlier are settled again from the latest cursor.
func (c Cursor) settle(e *Event) {
	if c.Codex != nil && e.Provider == "codex" {
		e.TerminalEvidence = c.Codex.TerminalAt > e.SourceCursor
	}
}

// Settle returns events with terminal evidence as of this cursor.
func (c Cursor) Settle(events []Event) []Event {
	out := append([]Event(nil), events...)
	for i := range out {
		c.settle(&out[i])
	}
	return out
}

// View returns the source's reasons as of this cursor, with held bytes read
// as pending, as NormalizeIncremental reports them.
func (c Cursor) View(held []byte) []string { return c.view(held) }
