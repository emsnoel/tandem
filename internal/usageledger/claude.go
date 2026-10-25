package usageledger

import "fmt"

// Claude usage. assistant records carry message.usage. One API message is
// written as several records sharing message.id, one per content block; each
// new record uuid is a revision and the latest replaces the earlier ones:
//
//	input_total       input_tokens + cache_creation_input_tokens + cache_read_input_tokens
//	cache_read        cache_read_input_tokens
//	cache_write       cache_creation_input_tokens (subtypes kept in components)
//	output            output_tokens
//	reasoning_output  output_tokens_details.thinking_tokens (a subset of output)
//
// Cache writes are input: they are never subtracted as if they were reads. An
// event is terminal when its latest revision has a stop_reason; the source is
// finished only when the last message stopped for end_turn, max_tokens,
// stop_sequence or refusal. A record repeated under the same uuid is ignored,
// and a different record under that uuid is a conflict.

var (
	claudeFields   = []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"}
	claudeMapped   = map[string]bool{"input_tokens": true, "cache_creation_input_tokens": true, "cache_read_input_tokens": true, "output_tokens": true, "cache_creation": true, "output_tokens_details": true}
	claudeTerminal = map[string]bool{"end_turn": true, "max_tokens": true, "stop_sequence": true, "refusal": true}
)

// claudeMsg is what the parser keeps of a message's latest revision.
type claudeMsg struct {
	Revision     int64    `json:"revision"`
	Reasons      []string `json:"reasons"`
	Terminal     bool     `json:"terminal"`
	Stop         string   `json:"stop,omitempty"`
	SessionKnown bool     `json:"session_known"`
}

// claudeState is the Claude parser's state between records. Seen holds a hash
// of each uuid's first record; Order is the messages in first-seen order.
type claudeState struct {
	Seen  map[string]string     `json:"seen"`
	Msgs  map[string]*claudeMsg `json:"msgs"`
	Order []string              `json:"order"`
}

func newClaudeState() *claudeState {
	return &claudeState{Seen: map[string]string{}, Msgs: map[string]*claudeMsg{}}
}

func (s *claudeState) events() int { return len(s.Order) }

func (s *claudeState) feed(r record, rs *reasons, emit func(Event)) {
	if t, _ := r.raw["type"].(string); t != "assistant" {
		return
	}
	if uuid, ok := r.raw["uuid"]; ok && uuid != nil {
		key, canon := canonical(uuid), digest(canonical(r.raw))
		if old, dup := s.Seen[key]; dup {
			if old != canon {
				rs.add("conflicting_record:%v", uuid)
			}
			return
		}
		s.Seen[key] = canon
	}
	message, _ := r.raw["message"].(map[string]any)
	id, _ := message["id"].(string)
	if id == "" {
		rs.add("missing_event_id:%d", r.cursor)
		return
	}
	revision := int64(0)
	if old := s.Msgs[id]; old != nil {
		revision = old.Revision + 1
	} else {
		s.Order = append(s.Order, id)
	}
	e := newEvent("claude", text(message["model"]), text(r.raw["sessionId"]), id, revision, r, nil)
	usage, _ := message["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	local := claudeCounters(&e, usage, r.cursor)
	stop := message["stop_reason"]
	e.TerminalEvidence = stop != nil
	stopText, _ := stop.(string)
	// Only the latest revision is authoritative, so a superseded revision's
	// reasons are not charged to the source.
	s.Msgs[id] = &claudeMsg{Revision: revision, Reasons: local, Terminal: e.TerminalEvidence, Stop: stopText,
		SessionKnown: e.SessionID != nil}
	emit(e)
}

func (s *claudeState) finish(rs *reasons) {
	missingSession := false
	for _, id := range s.Order {
		m := s.Msgs[id]
		rs.list = append(rs.list, m.Reasons...)
		if !m.Terminal {
			rs.add("message_not_final:%s", id)
		}
		missingSession = missingSession || !m.SessionKnown
	}
	if len(s.Order) > 0 && !claudeTerminal[s.Msgs[s.Order[len(s.Order)-1]].Stop] {
		rs.add("no_terminal_event")
	}
	if missingSession {
		rs.add("missing_session_id")
	}
}

// claudeCounters fills e's counters and components from usage and returns
// the reasons this revision is incomplete.
func claudeCounters(e *Event, usage map[string]any, cursor int) []string {
	var local []string
	values := map[string]*int64{}
	for _, f := range claudeFields {
		raw, present := usage[f]
		v, ok := counter(raw)
		switch {
		case !present:
			local = append(local, fmt.Sprintf("missing_counter:%s:%d", f, cursor))
		case !ok:
			local = append(local, fmt.Sprintf("invalid_counter:%s:%d", f, cursor))
		default:
			values[f] = ptr(v)
		}
	}
	ordinary, write, read := values["input_tokens"], values["cache_creation_input_tokens"], values["cache_read_input_tokens"]
	if ordinary != nil && write != nil && read != nil {
		sum, ok1 := add(*ordinary, *write)
		sum, ok2 := add(sum, *read)
		if ok1 && ok2 {
			e.InputTotal = ptr(sum)
		} else {
			local = append(local, fmt.Sprintf("counter_overflow:%d", cursor))
		}
	}
	e.CacheWrite, e.CacheRead, e.Output = write, read, values["output_tokens"]
	if details, ok := usage["output_tokens_details"].(map[string]any); ok {
		if raw := details["thinking_tokens"]; raw != nil {
			if v, ok := counter(raw); ok {
				e.ReasoningOutput = ptr(v)
			} else {
				local = append(local, fmt.Sprintf("invalid_counter:thinking_tokens:%d", cursor))
			}
		}
	}
	if e.ReasoningOutput != nil && e.Output != nil && *e.ReasoningOutput > *e.Output {
		local = append(local, fmt.Sprintf("reasoning_exceeds_output:%d", cursor))
	}
	subtypes, _ := usage["cache_creation"].(map[string]any)
	if subtypes == nil {
		subtypes = map[string]any{}
	}
	if len(subtypes) > 0 && e.CacheWrite != nil {
		var sum int64
		consistent := true
		for _, raw := range subtypes {
			v, ok := counter(raw)
			if consistent = consistent && ok; consistent {
				sum, consistent = add(sum, v)
			}
		}
		if !consistent || sum != *e.CacheWrite {
			local = append(local, fmt.Sprintf("cache_write_subtype_mismatch:%d", cursor))
		}
	}
	unmapped := map[string]any{}
	for k, v := range usage {
		if !claudeMapped[k] {
			unmapped[k] = v
		}
	}
	e.Components = map[string]any{"ordinary_input": ordinary, "cache_write_subtypes": subtypes, "unmapped": unmapped}
	return local
}
