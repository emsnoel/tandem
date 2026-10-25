package usageledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Codex usage. event_msg records of payload type token_count carry the
// session's cumulative payload.info.total_token_usage; a contribution is the
// difference from the previous valid total:
//
//	input_total       input_tokens (includes cached input)
//	cache_read        cached_input_tokens
//	cache_write       cache_write_input_tokens, nil when the log omits it
//	output            output_tokens
//	reasoning_output  reasoning_output_tokens (a subset of output)
//
// The raw totals stay in Components["cumulative"]. token_usage_record lines
// mirror the same usage per response and are not counted. Any counter that
// falls starts a new segment. Only a usage_reset record, written by the
// observer from external evidence, explains the fall; Codex logs have no
// native reset record, so otherwise the new segment's first contribution is
// unknown. An event is terminal once a later task_complete or turn_aborted
// record exists. A record repeated under the same ordinal is ignored, and a
// different record under that ordinal is a conflict.

const (
	codexInput      = "input_tokens"
	codexCached     = "cached_input_tokens"
	codexOutput     = "output_tokens"
	codexReasoning  = "reasoning_output_tokens"
	codexCacheWrite = "cache_write_input_tokens"
)

// codexFields are required. Reasoning and cache writes are optional: a log
// that omits one leaves only that counter unknown, not the others.
var codexFields = []string{codexInput, codexCached, codexOutput}

// codexState is the Codex parser's state between records. Seen holds a hash
// of each ordinal's first record, to find replays and conflicts.
type codexState struct {
	Session    *string           `json:"session,omitempty"`
	Model      *string           `json:"model,omitempty"`
	Seen       map[string]string `json:"seen"`
	Prev       map[string]int64  `json:"prev,omitempty"`
	PrevWrite  *int64            `json:"prev_write,omitempty"`
	PrevReason *int64            `json:"prev_reasoning,omitempty"`
	Segment    int               `json:"segment"`
	Index      int               `json:"index"`
	Reset      bool              `json:"reset"`
	TerminalAt int               `json:"terminal_at"`
	Emitted    int               `json:"emitted"`
	LastCursor int               `json:"last_cursor"`
}

func newCodexState() *codexState { return &codexState{Seen: map[string]string{}, TerminalAt: -1} }

func (s *codexState) events() int { return s.Emitted }

func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func (s *codexState) feed(r record, rs *reasons, emit func(Event)) {
	if ord, ok := r.raw["ordinal"]; ok && ord != nil {
		key, canon := canonical(ord), digest(canonical(r.raw))
		if old, dup := s.Seen[key]; dup {
			if old != canon {
				rs.add("conflicting_record:%v", ord)
			}
			return
		}
		s.Seen[key] = canon
	}
	kind, _ := r.raw["type"].(string)
	payload, _ := r.raw["payload"].(map[string]any)
	ptype, _ := payload["type"].(string)
	switch {
	case kind == "session_meta":
		if s.Session == nil {
			s.Session = text(payload["id"])
		}
	case kind == "turn_context":
		if v, ok := payload["model"]; ok {
			s.Model = text(v)
		}
	case kind == "usage_reset":
		s.Reset = true
	case kind == "event_msg" && (ptype == "task_complete" || ptype == "turn_aborted"):
		s.TerminalAt = r.cursor
	case kind == "event_msg" && ptype == "token_count":
		s.sample(r, payload, rs, emit)
	}
}

// sample handles one token_count record.
func (s *codexState) sample(r record, payload map[string]any, rs *reasons, emit func(Event)) {
	info, present := payload["info"]
	if !present || info == nil {
		return // a rate-limit-only snapshot carries no usage
	}
	infoMap, _ := info.(map[string]any)
	total, _ := infoMap["total_token_usage"].(map[string]any)
	if total == nil {
		rs.add("unsupported_usage_record:%d", r.cursor)
		return
	}
	vals := map[string]int64{}
	var invalid []string
	for _, f := range codexFields {
		if v, ok := counter(total[f]); ok {
			vals[f] = v
		} else {
			invalid = append(invalid, f)
		}
	}
	write, writeOK := counter(total[codexCacheWrite])
	reason, reasonOK := counter(total[codexReasoning])
	if raw, present := total[codexReasoning]; present && raw != nil && !reasonOK {
		invalid = append(invalid, codexReasoning)
	}
	decreased := false
	if s.Prev != nil && len(invalid) == 0 {
		for _, f := range codexFields {
			decreased = decreased || vals[f] < s.Prev[f]
		}
		decreased = decreased || (writeOK && s.PrevWrite != nil && write < *s.PrevWrite) ||
			(reasonOK && s.PrevReason != nil && reason < *s.PrevReason)
	}
	unexplained := decreased && !s.Reset
	if s.Reset || decreased {
		s.Segment, s.Index, s.Prev, s.PrevWrite, s.PrevReason, s.Reset = s.Segment+1, 0, nil, nil, nil, false
	}
	e := newEvent("codex", s.Model, s.Session, fmt.Sprintf("seg%d:%d", s.Segment, s.Index), 0, r,
		map[string]any{"cumulative": total, "segment": s.Segment})
	s.Index++
	defer func() {
		s.Emitted++
		s.LastCursor = r.cursor
		emit(e)
	}()
	if len(invalid) > 0 {
		for _, f := range invalid {
			rs.add("invalid_counter:%s:%d", f, r.cursor)
		}
		return
	}
	if vals[codexCached] > vals[codexInput] {
		rs.add("impossible_counter:%d", r.cursor)
		return
	}
	if unexplained {
		rs.add("unexplained_decrease:%d", r.cursor)
	} else {
		base := s.Prev
		if base == nil {
			base = map[string]int64{}
		}
		e.InputTotal = ptr(vals[codexInput] - base[codexInput])
		e.CacheRead = ptr(vals[codexCached] - base[codexCached])
		e.Output = ptr(vals[codexOutput] - base[codexOutput])
		switch {
		case reasonOK && s.Prev == nil:
			e.ReasoningOutput = ptr(reason)
		case reasonOK && s.PrevReason != nil:
			e.ReasoningOutput = ptr(reason - *s.PrevReason)
		}
		switch {
		case writeOK && s.Prev == nil:
			e.CacheWrite = ptr(write)
		case writeOK && s.PrevWrite != nil:
			e.CacheWrite = ptr(write - *s.PrevWrite)
		}
		if e.ReasoningOutput != nil && *e.ReasoningOutput > *e.Output {
			rs.add("reasoning_exceeds_output:%d", r.cursor)
		}
	}
	s.Prev = vals
	s.PrevWrite, s.PrevReason = nil, nil
	if writeOK {
		s.PrevWrite = ptr(write)
	}
	if reasonOK {
		s.PrevReason = ptr(reason)
	}
}

func (s *codexState) finish(rs *reasons) {
	if s.Emitted > 0 && s.Session == nil {
		rs.add("missing_session_id")
	}
	if s.Emitted > 0 && s.TerminalAt <= s.LastCursor {
		rs.add("no_terminal_event")
	}
}
