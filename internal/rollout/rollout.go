// Package rollout reads Codex rollout logs and reports where a session's tokens
// went, phase by phase. A phase is the span after one task_started event: one
// prompt and everything the agent did about it. It shows how large the context
// was when each phase began and how many calls went to polling.
//
// Pure: it parses what it is given. cmd/tandem opens the files.
package rollout

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Phase is one task's worth of API calls.
type Phase struct {
	File         string `json:"file"`
	Index        int    `json:"phase"`
	Calls        int    `json:"calls"`
	FirstContext int64  `json:"first_context"`
	Peak         int64  `json:"peak"`
	Input        int64  `json:"input"`
	Cached       int64  `json:"cached"`
	Output       int64  `json:"output"`
	// Polls counts write_stdin calls: each one re-sends the whole context to
	// check on a command that was started with too short a wait.
	Polls int `json:"polls"`
	// Yields counts each yield_time_ms value the agent asked for. Short values
	// here next to a high Polls says the guidance was ignored; long values next
	// to a high Polls says it was followed and did not work.
	Yields map[string]int `json:"yields"`
}

type event struct {
	Type    string `json:"type"`
	Payload struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
		Info      *struct {
			Last *struct {
				Input  int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// Code mode wraps tool calls in JavaScript (`tools.write_stdin({...})`), where
// keys may be quoted or bare; plain function calls carry JSON arguments.
var yieldRE = regexp.MustCompile(`yield_time_ms"?\s*:\s*([0-9]+)`)

// Parse reads one rollout log. A final line that does not decode is ignored,
// because a session still being written ends mid-line; any other undecodable
// line is an error.
func Parse(r io.Reader, file string) ([]Phase, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	var phases []Phase
	cur := func() *Phase {
		if len(phases) == 0 {
			phases = append(phases, Phase{File: file, Index: 0, Yields: map[string]int{}})
		}
		return &phases[len(phases)-1]
	}

	var pending error // a decode error, forgiven only if no line follows it
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		if pending != nil {
			return nil, pending
		}
		var e event
		if err := json.Unmarshal(raw, &e); err != nil {
			pending = fmt.Errorf("%s line %d: %w", file, n, err)
			continue
		}
		p := e.Payload
		switch {
		case e.Type == "event_msg" && p.Type == "task_started":
			phases = append(phases, Phase{File: file, Index: nextIndex(phases), Yields: map[string]int{}})
		case e.Type == "event_msg" && p.Type == "token_count":
			if p.Info == nil || p.Info.Last == nil {
				continue
			}
			ph, u := cur(), p.Info.Last
			ph.Calls++
			ph.Input += u.Input
			ph.Cached += u.Cached
			ph.Output += u.Output
			if ph.FirstContext == 0 {
				ph.FirstContext = u.Input
			}
			if u.Input > ph.Peak {
				ph.Peak = u.Input
			}
		case e.Type == "response_item" && p.Type == "custom_tool_call":
			ph := cur()
			ph.Polls += strings.Count(p.Input, "tools.write_stdin(")
			countYields(ph, p.Input)
		case e.Type == "response_item" && p.Type == "function_call":
			ph := cur()
			if p.Name == "write_stdin" {
				ph.Polls++
			}
			countYields(ph, p.Arguments)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return phases, nil
}

func nextIndex(ps []Phase) int {
	if len(ps) == 0 {
		return 1
	}
	return ps[len(ps)-1].Index + 1
}

func countYields(ph *Phase, s string) {
	for _, m := range yieldRE.FindAllStringSubmatch(s, -1) {
		ph.Yields[m[1]]++
	}
}

// Total sums phases, possibly from several files. FirstContext is the first
// phase's; Peak is the largest seen.
func Total(ps []Phase) Phase {
	t := Phase{File: "TOTAL", Yields: map[string]int{}}
	for i, p := range ps {
		if i == 0 {
			t.FirstContext = p.FirstContext
		}
		t.Calls += p.Calls
		t.Input += p.Input
		t.Cached += p.Cached
		t.Output += p.Output
		t.Polls += p.Polls
		if p.Peak > t.Peak {
			t.Peak = p.Peak
		}
		for k, v := range p.Yields {
			t.Yields[k] += v
		}
	}
	return t
}
