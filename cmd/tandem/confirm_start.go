package main

// Whether a prompt typed into a settled pane started a turn that is working.
//
// Herdr's status shows a turn started, not that it can run. A pane on a
// rejected model, an expired login or no quota goes busy and is idle again
// about a second later. No real task finishes within failWithin, so a pane
// settled again by then failed, unless its log shows model work. What the pane
// shows is passed on for a person to read, never parsed.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// failWithin is how long a started turn must keep running to count. The Claude
// model error appears about a second after the start.
const failWithin = 8 * time.Second

// failWithinFromEnv lets TANDEM_FAIL_WITHIN_MS shorten failWithin for the shell
// tests. A value that is not a positive whole number keeps the default.
func failWithinFromEnv() time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("TANDEM_FAIL_WITHIN_MS")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return failWithin
}

// paneLines is how much of the pane a turn that ended at once reports.
const paneLines = 20

type startOutcome string

const (
	started      startOutcome = "started"
	neverStarted startOutcome = "never_started"
	endedAtOnce  startOutcome = "ended_at_once"
)

type startResult struct {
	outcome startOutcome
	// agent is the pane as last seen once the turn started.
	agent *herdr.Agent
	// paneText is the end of the pane, for a turn that ended at once.
	paneText string
	// readErr is set when the start was not seen and the closing read failed:
	// Herdr did not answer, which says nothing about the prompt.
	readErr error
}

// tokenMark is a conversation's token total read from its log just before a
// prompt is typed, the baseline a turn that ends at once is judged against.
type tokenMark struct {
	session string
	total   int64
	ok      bool
	// known: the turn runs in session whatever Herdr shows, because a reset proved
	// it from the CLI's own record (Copilot).
	known bool
	// marker is text the prompt carries (its nonce) and since is when it was typed.
	// Where Herdr names no conversation (Codex), the one whose log holds the
	// marker is the turn's.
	marker string
	since  time.Time
}

// markTokens reads a's conversation's token total. A pane with no conversation
// yet has none to read; a turn there starts a new one, whose baseline is 0.
func markTokens(d herdrDeps, a *herdr.Agent) tokenMark {
	s := a.Conversation()
	if s == "" || d.tokens == nil {
		return tokenMark{}
	}
	total, ok := d.tokens(a.Kind, s)
	return tokenMark{session: s, total: total, ok: ok}
}

// confirmStart watches a pane after a prompt was typed into it while settled.
// w was opened and snapshotted before the prompt, so no change is missed. The
// turn starts at the first unsettled status, and counts once it is still
// unsettled failWithin later, or if its log shows it used tokens. Each phase
// ends on its event or deadline with one closing read, so a broken stream
// cannot hang a send.
func confirmStart(d herdrDeps, w eventWatch, name string, before *herdr.Agent, mark tokenMark) startResult {
	if ok, _, rerr := awaitStart(d, w, name); !ok {
		return startResult{outcome: neverStarted, readErr: rerr}
	}
	within := d.failWithin
	if within <= 0 {
		within = failWithin
	}
	ended, cur, rerr := awaitStatus(d.get, d.now, d.sleep, w, within, agent.Settled, name)
	if cur == nil {
		// The wait ended on an event, or its closing read failed: either way the
		// receipt needs the agent as it is now.
		var err error
		if rerr == nil {
			cur, err = d.get(name)
		}
		if rerr != nil || err != nil {
			// Gone after it started: it did not keep running.
			cur, ended = before, true
		}
	}
	if !ended {
		return startResult{outcome: started, agent: cur}
	}
	if usedTokens(d, before, cur, mark) {
		return startResult{outcome: started, agent: cur}
	}
	return startResult{outcome: endedAtOnce, agent: cur, paneText: paneTail(d, name)}
}

// awaitStart waits up to landWithin for a prompt typed into a settled pane
// to start a turn: its first status that is not settled.
func awaitStart(d herdrDeps, w eventWatch, name string) (bool, *herdr.Agent, error) {
	return awaitStatus(d.get, d.now, d.sleep, w, landWithin, func(st string) bool { return !agent.Settled(st) }, name)
}

// usedTokens reports whether a turn that ended within failWithin did model
// work, or might have. Time alone cannot tell, because a small review can take
// seconds. No new tokens in the log means no model call. An unreadable log
// counts as yes: blocking real work costs more than a late warning.
func usedTokens(d herdrDeps, before, cur *herdr.Agent, mark tokenMark) bool {
	session, kind := cur.Conversation(), cur.Kind
	if kind == "" {
		kind = before.Kind
	}
	if mark.known {
		session = mark.session
	}
	if session == "" && mark.marker != "" && d.find != nil {
		session = d.find(kind, mark.marker, mark.since)
	}
	if session == "" || d.tokens == nil {
		return true
	}
	// The baseline is the same conversation's total before the prompt; a turn
	// that runs in a new conversation, after a reset, starts from 0.
	var base int64
	if session == mark.session {
		if !mark.ok {
			return true
		}
		base = mark.total
	}
	now, ok := d.tokens(kind, session)
	return !ok || now > base
}

func paneTail(d herdrDeps, name string) string {
	return paneTailWith(func() (string, error) { return d.read(name) })
}

// paneTailWith is the end of a pane as read, without Herdr's line padding.
func paneTailWith(read func() (string, error)) string {
	text, err := read()
	if err != nil {
		return "(the pane could not be read: " + err.Error() + ")"
	}
	// Herdr pads each line to the pane's width; quoted with it, a line wraps.
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// conversationTokens reads a conversation's token total, input and output,
// from its CLI's own logs, the session's and everything it spawned: the same
// reader the durable run uses for usage. ok is false when its log cannot be
// found or read, or the kind has no log reader.
func conversationTokens(src controller.UsageSource) func(kind, session string) (int64, bool) {
	return func(kind, session string) (int64, bool) {
		streams, err := src.Collect(context.Background(), kind, session, time.Time{}, time.Now())
		// A log that was read but holds no usage events is a conversation that
		// has made no model call yet: 0, not unknown.
		if err != nil || len(streams) == 0 || (streams[0].Unavailable != "" && !streams[0].NoUsageEvents) {
			return 0, false
		}
		var total int64
		for _, st := range streams {
			for _, dl := range st.Deltas {
				total += dl.Counters.Input + dl.Counters.Output
			}
		}
		return total, true
	}
}

func tailOfPane(d herdrDeps, pane string) string {
	if pane == "" {
		return "(the pane is not known)"
	}
	return paneTailWith(func() (string, error) { return d.readPane(pane) })
}

// agentAliveCmd is `tandem agent alive NAME`: exit 0 when the agent is still
// running after the watch, AgentExited with code<TAB>message and the pane on
// stderr when its CLI quit.
func agentAliveCmd(d herdrDeps, name string, within time.Duration, stderr io.Writer) int {
	alive, why, text := agentAlive(d, name, within)
	if alive {
		return exitcode.OK
	}
	fmt.Fprintf(stderr, "agent_exited\t%s started, then %s\n%s\n", name, why, text)
	return exitcode.AgentExited
}
