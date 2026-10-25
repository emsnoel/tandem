package controller

// Usage from Claude Code's own logs.
//
// Claude Code writes each conversation to <projects>/<project>/<session>.jsonl
// and each subagent to <session>/subagents/*.jsonl. A parent's log excludes its
// subagents, so each is its own stream. Parsing is internal/usageledger's, so
// the run and the ledger count a message the same way: one event per message
// id, its latest revision.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/usageledger"
)

const providerClaude = "claude"

// claudeSessionID is what a Claude session id looks like. It names a file, so
// nothing else is ever put in a path.
var claudeSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// DefaultClaudeProjects is where Claude Code files its conversation logs:
// $CLAUDE_CONFIG_DIR/projects, or ~/.claude/projects.
func DefaultClaudeProjects() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// claudeStreams returns the session's log and its subagents' logs, the session
// first. A session with no log, or an id that is not one, is one unavailable
// stream.
func claudeStreams(projectsDir, session string) []rollout.Stream {
	gap := func(why string) []rollout.Stream {
		return []rollout.Stream{{Provider: providerClaude, SessionID: session, Unavailable: why}}
	}
	if !claudeSessionID.MatchString(session) {
		return gap("the session id is not a Claude session id, so no log was looked for")
	}
	found, _ := filepath.Glob(filepath.Join(projectsDir, "*", session+".jsonl"))
	if len(found) == 0 {
		return gap("no Claude log was found for this session")
	}
	sort.Strings(found)
	main := found[0]
	out := []rollout.Stream{readClaudeStream(main, session, "")}
	if len(found) > 1 {
		out[0].Diagnostics = append(out[0].Diagnostics, "more than one project holds this session's log; read "+main)
	}
	subs, _ := filepath.Glob(filepath.Join(filepath.Dir(main), session, "subagents", "*.jsonl"))
	sort.Strings(subs)
	for _, f := range subs {
		name := filepath.Base(f)
		out = append(out, readClaudeStream(f, session+"/"+name[:len(name)-len(".jsonl")], session))
	}
	return out
}

// messageSourceID keys a Claude event by its message id, not its line: a
// message's final revision can be on any line, and reading the log again must
// give the same id, so a stored record is matched and never added to. Same
// shape as rollout's source ids.
func messageSourceID(session, file, messageID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|message:%s", providerClaude, session, filepath.Base(file), messageID)))
	return hex.EncodeToString(sum[:16])
}

func readClaudeStream(path, session, parent string) rollout.Stream {
	s := rollout.Stream{Provider: providerClaude, SessionID: session, ParentID: parent, File: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		s.Unavailable = "the log could not be read: " + err.Error()
		return s
	}
	batch, err := usageledger.Normalize(providerClaude, raw)
	if err != nil {
		s.Unavailable = "the log could not be parsed: " + err.Error()
		return s
	}
	for _, e := range batch.Events {
		// A message still being written can change, and a stored record cannot.
		// It is recorded on a later read, once it has a stop reason; until then
		// the stream is partial (batch.Complete is false).
		if !e.TerminalEvidence {
			continue
		}
		if e.InputTotal == nil || e.CacheRead == nil || e.Output == nil {
			s.Rejected++
			continue
		}
		model := ""
		if e.Model != nil {
			model = *e.Model
		}
		s.Deltas = append(s.Deltas, rollout.Delta{SourceID: messageSourceID(session, path, e.EventID), Line: e.SourceCursor,
			Model: model, Counters: rollout.Counters{Input: *e.InputTotal, Cached: *e.CacheRead, Output: *e.Output}})
	}
	// A log whose last message has not finished is still being written.
	if !batch.Complete {
		s.Truncated = true
		s.Diagnostics = append(s.Diagnostics, batch.Reasons...)
	}
	return s
}
