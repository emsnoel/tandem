package main

// One way to type a prompt into an agent's pane, for every sender: the durable
// run's transport (build_transport.go) and `tandem agent prompt --as`
// (confirm_prompt.go), which the bash senders and the bridge call. A check
// that guards a delivery, such as the screen guard, lands here once.
//
// What differs between two kinds of message is their deliverPolicy. Every
// kind's policy is a row in one of the tables in this file, so a difference in
// how two messages are delivered is chosen and visible, never an accident of
// which path a message took.

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/herdr"
)

// deliverPolicy is how one kind of message is delivered. The zero value types
// the prompt after one read into any pane not at a dialog, and watches nothing.
type deliverPolicy struct {
	// settled refuses a pane mid-turn. Without it, a prompt typed into a
	// running turn queues behind it.
	settled bool
	// reset gives the pane a fresh conversation first (a task).
	reset bool
	// confirm watches a pane that was settled until the turn is seen to run
	// (confirm_start.go).
	confirm bool
	// wait is how long a dialog, or a screen whose input is not the
	// conversation's, is waited out before giving up. 0 gives up at once.
	wait time.Duration
}

// effectPolicies is every durable effect the transport types into a pane. None
// waits: a refused effect is tried again on the run's next tick.
var effectPolicies = map[string]deliverPolicy{
	"builder_dispatch": {settled: true, reset: true, confirm: true},
	"review_prompt":    {settled: true, confirm: true},
	// A notice's receipt finishes the run (controller/effects.go), so it is typed
	// and not watched. The planner's verification and recovery requests are sent
	// the same way.
	"notify_planner":    {},
	"verify_prompt":     {},
	"recovery_decision": {},
	// A cancel must reach a builder mid-turn.
	"cancel": {},
}

// senderPolicies is every kind `tandem agent prompt --as KIND` delivers, for
// the bash senders and the bridge. Each is confirmed, so a script that typed
// a prompt can say whether a turn ran on it. --wait-unblocked replaces wait.
var senderPolicies = map[string]deliverPolicy{
	// To the builder or reviewer. Mid-turn, a task or review typed here queues;
	// the durable run refuses instead (effectPolicies).
	"task":   {confirm: true},
	"review": {confirm: true},
	// To the planner, which a person may be using: a dialog or a subagent's screen
	// is waited out for up to two minutes.
	"handback": {confirm: true, wait: 2 * time.Minute},
	"verdict":  {confirm: true, wait: 2 * time.Minute},
	"question": {confirm: true, wait: 2 * time.Minute},
	"notice":   {confirm: true, wait: 2 * time.Minute},
}

// senderKinds is the kinds --as takes, sorted.
func senderKinds() []string { return slices.Sorted(maps.Keys(senderPolicies)) }

// deliverReq is one prompt to deliver.
type deliverReq struct {
	name, text string
	// nonce is the effect's nonce, which text already carries. Without one, a
	// confirmed prompt to a pane whose conversation Herdr does not name gets a
	// marker of its own.
	nonce string
	// root is the checkout a reset runs in. shareContext lets a pane with no
	// verified reset be sent a task in the conversation it has.
	root         string
	shareContext bool
}

// deliverOutcome is what became of a prompt.
type deliverOutcome int

const (
	// sendRefused: nothing was typed. A refusal of tandem's own has a code.
	sendRefused deliverOutcome = iota
	// sendTyped: typed, and by policy not watched.
	sendTyped
	// sendQueued: typed into a running turn, so there was nothing to watch.
	sendQueued
	// sendStarted: a turn started and did work.
	sendStarted
	// sendEndedAtOnce: the turn ended at once with no model work, so nothing
	// is working on the prompt.
	sendEndedAtOnce
	// sendNeverStarted: typed, but no turn was seen to start. It may sit in a
	// dialog Herdr cannot see.
	sendNeverStarted
	// sendUncertain: the prompt may have been typed, but Herdr did not answer.
	sendUncertain
)

type deliverResult struct {
	outcome deliverOutcome
	// code names a refusal tandem made itself (agent_blocked, agent_busy,
	// agent_input_elsewhere, reset_unverified, reset_failed). It is "" when
	// err is Herdr's own, which carries its code.
	code string
	err  error
	// agent is the pane as last seen: before typing, or once its turn started.
	agent *herdr.Agent
	// before is the pane's conversation before any reset. reset says the
	// conversation is fresh, and proved names it when the CLI's own record did.
	before string
	reset  bool
	proved string
	// paneText is the end of the pane, for a turn that ended at once.
	paneText string
}

func declined(code, format string, a ...any) deliverResult {
	return deliverResult{outcome: sendRefused, code: code, err: fmt.Errorf(format, a...)}
}

func notTyped(err error) deliverResult { return deliverResult{outcome: sendRefused, err: err} }

// deliverPrompt types r.text into r.name's pane as p says and reports what
// became of it. Nothing is typed into a pane at a dialog, or showing a screen
// its input does not reach; a policy with a wait gives either that long to
// clear.
func deliverPrompt(d herdrDeps, r deliverReq, p deliverPolicy) deliverResult {
	name, start := r.name, d.now()
	// Only a delivery that waits on the pane needs its events. The others
	// type and return, so one plain read is enough.
	watched := p.confirm || p.reset || p.wait > 0
	var w eventWatch
	defer func() {
		if w != nil {
			w.Close()
		}
	}()
	var a *herdr.Agent
	// look reads the pane afresh. A watch is replaced, because a snapshot
	// taken before a wait or a reset predates what they changed.
	look := func() error {
		if !watched {
			var err error
			a, err = d.get(name)
			return err
		}
		if w != nil {
			w.Close()
			w = nil
		}
		nw, err := d.watch(name)
		if err != nil {
			return err
		}
		w, a = nw, nw.Snapshot(name)
		return nil
	}
	if err := look(); err != nil {
		return notTyped(err)
	}
	if a.StatusString() == "blocked" && p.wait > 0 {
		// No sleep on a broken stream: nothing has been typed yet, and waiting would
		// hold the bridge's queue.
		noSleep := func(time.Duration) {}
		ok, _, err := awaitStatus(d.get, d.now, noSleep, w, p.wait, func(st string) bool { return st != "blocked" }, name)
		if err != nil {
			return notTyped(err)
		}
		if !ok {
			return declined("agent_blocked", "%s is still at a dialog after %v; nothing was typed", name, p.wait)
		}
		if err := look(); err != nil {
			return notTyped(err)
		}
	}
	switch st := a.StatusString(); {
	case st == "blocked":
		return declined("agent_blocked", "%s is at a dialog, where a prompt would land; nothing was typed", name)
	case p.settled && !agent.Settled(st):
		return declined("agent_busy", "%s is %s, so a prompt typed now would land in a running turn; nothing was typed", name, st)
	}
	// A pane showing a subagent's screen, or a picker, reads idle too, and
	// what is typed there misses the conversation. It gets what is left
	// of the same wait as a dialog.
	if why, waited := awaitMainInput(d, a.Kind, name, max(0, p.wait-d.now().Sub(start))); why != "" {
		return declined("agent_input_elsewhere", "%s", offMainRefusal(name, why))
	} else if waited {
		if err := look(); err != nil {
			return notTyped(err)
		}
	}

	res := deliverResult{before: a.Conversation()}
	if p.reset {
		tok, session, err := d.reset(name, r.root)
		if err != nil {
			return notTyped(fmt.Errorf("resetting %s: %w", name, err))
		}
		switch tok {
		case "reset", "sent", "skip:no-session":
			// A conversation that was reset, or that does not exist yet, is fresh.
			res.reset, res.proved = true, session
		case "skip:unsupported", "skip:no-identity", "skip:holds-build":
			if !r.shareContext {
				return declined("reset_unverified", "a fresh conversation for %s cannot be verified (%s), so the task was not sent. Start a new builder pane, or start the run with --share-context to accept the conversation it has", name, tok)
			}
		default: // unconfirmed, skip:not-idle, skip:off-main, or anything unknown
			return declined("reset_failed", "the conversation of %s could not be reset (%s), so the task was not sent", name, tok)
		}
		// The reset produced events the confirm must not read.
		if err := look(); err != nil {
			return notTyped(err)
		}
		if st := a.StatusString(); !agent.Settled(st) {
			return declined("agent_busy", "%s is %s after its reset", name, st)
		}
	}

	text := r.text
	var mark tokenMark
	if p.confirm {
		mark = markTokens(d, a)
		if res.proved != "" {
			// The turn runs in the conversation the reset proved, new and so
			// at 0 tokens, whatever id Herdr still shows.
			mark = tokenMark{session: res.proved, ok: true, known: true}
		}
		// Where Herdr names no conversation, the turn's is found in the CLI's
		// logs by text the prompt carries: the effect's nonce, or a marker
		// added here, since a script's prompt carries none.
		switch {
		case r.nonce != "":
			mark.marker, mark.since = r.nonce, d.now()
		case !herdr.NamesConversation(a.Kind):
			if m, err := newSessionID(); err == nil {
				text += "\n\n(tandem delivery " + m + ")"
				mark.marker, mark.since = m, d.now()
			}
		}
	}
	if err := d.prompt(name, text); err != nil {
		// Herdr declining the prompt typed nothing. A command that got no
		// answer, or died, may have typed it.
		var he *herdr.Error
		if errors.As(err, &he) && he.Code != "unreachable" {
			return notTyped(err)
		}
		res.outcome, res.err = sendUncertain, err
		return res
	}
	res.agent = a
	switch {
	case !agent.Settled(a.StatusString()):
		// Typed into a running turn, the prompt queues behind it. Its status
		// cannot show whether it landed, so there is nothing to watch.
		res.outcome = sendQueued
		return res
	case !p.confirm:
		res.outcome = sendTyped
		return res
	}
	cs := confirmStart(d, w, name, a, mark)
	switch {
	case cs.readErr != nil:
		// Typed, so whether it arrived is unknown. It is never refused, which
		// would let it be typed again.
		res.outcome, res.err = sendUncertain, cs.readErr
	case cs.outcome == started:
		res.outcome, res.agent = sendStarted, cs.agent
	case cs.outcome == endedAtOnce:
		res.outcome, res.agent, res.paneText = sendEndedAtOnce, cs.agent, cs.paneText
	default:
		res.outcome = sendNeverStarted
	}
	return res
}
