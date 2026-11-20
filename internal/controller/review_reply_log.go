package controller

// A reviewer's reply, read from its CLI's own conversation log.
//
// A terminal is a lossy record of a reply. Codex redraws its screen, so Herdr
// can read a pane from the middle of a reply on, past job_id and the hashes
// (issue #86). Each CLI also writes every reply whole to its log, so the
// controller reads the reply there first and falls back to the pane.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/workflow"
)

// ReplyReader is a UsageSource that can also read what an agent replied.
type ReplyReader interface {
	// AgentReplies is the text of every reply in one conversation's log,
	// oldest first. session is the conversation Herdr named, "" when it named
	// none; marker is text the conversation's prompt carried, which finds a
	// conversation Herdr does not name. A conversation with no log found is
	// "". A kind it cannot read is workflow.ErrUnsupported.
	AgentReplies(ctx context.Context, kind, session, marker string, from, to time.Time) (string, error)
}

// AgentReplies reads Codex, Claude and Copilot logs. Herdr names only Claude
// conversations; a Codex or Copilot one is found by marker in its prompts.
func (a AgentUsageSource) AgentReplies(ctx context.Context, kind, session, marker string, from, to time.Time) (string, error) {
	switch kind {
	case "codex":
		if session == "" {
			s, err := a.SessionWith(ctx, kind, marker, from, to)
			if err != nil || s == "" {
				return "", err
			}
			session = s
		}
		path, err := rollout.FindSession(a.CodexSessions, session)
		if err != nil || path == "" {
			return "", err
		}
		return readReplies(path, rollout.Replies)
	case "claude":
		if !claudeSessionID.MatchString(session) {
			return "", nil
		}
		found, _ := filepath.Glob(filepath.Join(a.ClaudeProjects, "*", session+".jsonl"))
		if len(found) == 0 {
			return "", nil
		}
		sort.Strings(found)
		return readReplies(found[0], claudeReplies)
	case "copilot":
		s, err := a.SessionWith(ctx, kind, marker, from, to)
		if err != nil || s == "" {
			return "", err
		}
		return readReplies(copilotEvents(a.CopilotHome, s), copilotReplies)
	}
	return "", fmt.Errorf("%w: replies are read from Codex, Claude and Copilot logs only, not %s", workflow.ErrUnsupported, kind)
}

func readReplies(path string, parse func(io.Reader) (string, error)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return parse(f)
}

// eachLine calls fn with each line of a JSONL log.
func eachLine(r io.Reader, fn func([]byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}

// claudeReplies is the text blocks of a Claude Code log's assistant messages.
func claudeReplies(r io.Reader) (string, error) {
	var b strings.Builder
	err := eachLine(r, func(line []byte) {
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" {
			return
		}
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
				b.WriteString("\n")
			}
		}
	})
	return b.String(), err
}

// copilotReplies is the content of a Copilot log's assistant.message events.
func copilotReplies(r io.Reader) (string, error) {
	var b strings.Builder
	err := eachLine(r, func(line []byte) {
		var e struct {
			Type string `json:"type"`
			Data struct {
				Content string `json:"content"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant.message" || e.Data.Content == "" {
			return
		}
		b.WriteString(e.Data.Content)
		b.WriteString("\n")
	})
	return b.String(), err
}
