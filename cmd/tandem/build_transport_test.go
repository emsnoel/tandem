package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

// fakePane is one Herdr agent as the transport sees it. Typing a prompt into it
// moves it to "working" (and counts a lifecycle change) unless swallow is set,
// which is a prompt that landed in a dialog Herdr cannot see.
type fakePane struct {
	kind    string // the agent Herdr detects; "" is claude, the kind whose session Herdr names
	status  string
	seq     int
	session string
	swallow bool

	getErr    error
	promptErr error
	resetTok  string
	resetErr  error
	// resetSession is the conversation the reset proved from the CLI's own
	// record, as Copilot's process log names it.
	resetSession string
	// logged is the conversation whose log holds the prompt's nonce, as
	// Codex's own logs show it when Herdr names none.
	logged string
	// searched is the last marker a conversation was looked up by.
	searched string

	// newSession is the conversation a reset starts. Herdr may learn its id
	// late, so it shows the new id after lag further reads (lag < 0: never in
	// the test).
	newSession     string
	lag            int
	getsSinceReset int

	// endAfter > 0 is a turn that fails as soon as it starts, as one on a model
	// the CLI rejects: the endAfter-th read after the prompt finds the pane
	// settled again. paneText is what the pane shows then.
	endAfter        int
	getsSincePrompt int
	paneText        string

	// screens is what the pane's screen shows, one per read, repeating the
	// last; nil is the main conversation's input. screenErr is a screen
	// Herdr cannot read.
	screens   []string
	screenErr error
	shown     int

	// tokenLog is each conversation's token total as its log shows it, and
	// promptTokens what a prompt's turn adds: 0 is a turn that made no model
	// call, as one on a rejected model. unreadable is a log tandem cannot read.
	tokenLog     map[string]int64
	promptTokens int64
	unreadable   bool

	// goneAfter > 0 is an agent that quits at launch: from that many reads on,
	// Herdr no longer lists it. shellAfter > 0 leaves it listed but its pane's
	// foreground process is the shell again. reads counts every read.
	goneAfter, shellAfter, reads int
	// unblockAfter > 0 is a dialog that clears on that many reads.
	unblockAfter int

	prompts []string
	// promptsSeen is how many prompts the watch has reported as working.
	promptsSeen int
	// seqFrozen: Herdr's lifecycle counter does not move, so only events show a turn.
	seqFrozen bool
	resets    int
	now       time.Time
}

// read is the pane's state machine: one Herdr read, with every counter.
func (p *fakePane) read(name string) (*herdr.Agent, error) {
	if p.getErr != nil {
		return nil, p.getErr
	}
	p.reads++
	if p.goneAfter > 0 && p.reads >= p.goneAfter {
		return nil, &herdr.Error{Code: "agent_not_found", Message: "agent target " + name + " not found"}
	}
	if p.unblockAfter > 0 && p.status == "blocked" {
		p.unblockAfter--
		if p.unblockAfter == 0 {
			p.status = "idle"
		}
	}
	st := p.status
	if p.resets > 0 && p.newSession != "" {
		p.getsSinceReset++
		if p.lag >= 0 && p.getsSinceReset > p.lag {
			p.session = p.newSession
		}
	}
	if len(p.prompts) > 0 && p.endAfter > 0 {
		p.getsSincePrompt++
		if p.getsSincePrompt == p.endAfter {
			p.status = "idle"
			if !p.seqFrozen {
				p.seq++
			}
		}
		st = p.status
	}
	kind := p.kind
	if kind == "" {
		kind = "claude"
	}
	return &herdr.Agent{PaneID: "%1", Name: &name, Kind: kind, Status: &st, StateChangeSeq: p.seq, Session: &herdr.AgentSession{Value: p.session}}, nil
}

func (p *fakePane) deps() herdrDeps {
	return herdrDeps{
		get: p.read,
		watch: func(names ...string) (eventWatch, error) {
			w, err := newStepWatch(names[0], p.read, func(d time.Duration) { p.now = p.now.Add(d) })
			if err != nil {
				return nil, err
			}
			// A prompt typed into a settled pane reports working at once, as Herdr
			// does, even when the fake's next read already finds the turn over.
			w.pending = func() []herdr.Event {
				if len(p.prompts) == p.promptsSeen {
					return nil
				}
				p.promptsSeen = len(p.prompts)
				if p.swallow {
					return nil
				}
				return []herdr.Event{{Kind: "pane_agent_status_changed", Name: names[0], PaneID: "%1", Status: "working"}}
			}
			return w, nil
		},
		prompt: func(name, text string) error {
			if p.promptErr != nil {
				return p.promptErr
			}
			p.prompts = append(p.prompts, name+": "+text)
			if p.promptTokens > 0 {
				if p.tokenLog == nil {
					p.tokenLog = map[string]int64{}
				}
				// The turn's conversation: after a reset, the new one.
				sess := p.session
				if p.resets > 0 && p.newSession != "" {
					sess = p.newSession
				}
				if p.resets > 0 && p.resetSession != "" {
					sess = p.resetSession
				}
				if p.logged != "" {
					sess = p.logged
				}
				p.tokenLog[sess] += p.promptTokens
			}
			if !p.swallow {
				p.status, p.seq = "working", p.seq+1
			}
			return nil
		},
		reset: func(name, root string) (string, string, error) {
			p.resets++
			return p.resetTok, p.resetSession, p.resetErr
		},
		read: func(name string) (string, error) {
			return p.paneText, nil
		},
		screen: func(name string) (string, error) {
			if p.screenErr != nil || p.screens == nil {
				return "", p.screenErr
			}
			s := p.screens[min(p.shown, len(p.screens)-1)]
			p.shown++
			return s, nil
		},
		shellForeground: func(pane string) (bool, error) {
			return p.shellAfter > 0 && p.reads >= p.shellAfter, nil
		},
		readPane: func(pane string) (string, error) {
			return p.paneText, nil
		},
		tokens: func(kind, session string) (int64, bool) {
			if p.unreadable {
				return 0, false
			}
			return p.tokenLog[session], true
		},
		find: func(kind, marker string, since time.Time) string {
			p.searched = marker
			if marker == "" || len(p.prompts) == 0 {
				return ""
			}
			return p.logged
		},
		readPrompt: func(_ context.Context, hash string) (string, error) {
			if hash == "missing" {
				return "", errors.New("no such artifact")
			}
			return "PROMPT " + hash, nil
		},
		sleep: func(d time.Duration) { p.now = p.now.Add(d) },
		now:   func() time.Time { return p.now },
	}
}

func newPane(status string) *fakePane {
	return &fakePane{status: status, session: "s1", resetTok: "reset", now: time.Unix(1_700_000_000, 0)}
}

func transportFor(p *fakePane) *herdrTransport {
	return newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: "/repo"}, p.deps())
}

func delivery(kind, session string) runner.Delivery {
	return runner.Delivery{Kind: kind, EffectID: "e1", Nonce: "n1", PromptHash: "h1", AgentKind: "codex", SessionID: session}
}

func TestHerdrSendBuilderDispatchResetsThenTypesThePrompt(t *testing.T) {
	p := newPane("idle")
	p.newSession = "s2"
	rc, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if err != nil {
		t.Fatal(err)
	}
	if p.resets != 1 || len(p.prompts) != 1 || p.prompts[0] != "builder: PROMPT h1" {
		t.Fatalf("resets %d prompts %q", p.resets, p.prompts)
	}
	if rc.EffectID != "e1" || rc.ExternalID == "" {
		t.Fatalf("receipt %+v", rc)
	}
}

// Herdr may report a new session id late, still showing the conversation just
// left. The receipt must not name that id. Only a Claude pane's id is read.
func TestHerdrReceiptNamesTheNewConversationOrNone(t *testing.T) {
	for name, tc := range map[string]struct {
		newSession string
		lag        int
		want       string
	}{
		"the id changes at once":        {"s2", 0, "s2"},
		"the id changes a little later": {"s2", 3, "s2"},
		"the id never changes in time":  {"s2", -1, ""},
		"no new id is ever reported":    {"", 0, ""},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPane("idle")
			p.newSession, p.lag = tc.newSession, tc.lag
			rc, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
			if err != nil {
				t.Fatal(err)
			}
			if rc.ExternalID != tc.want {
				t.Errorf("external id %q, want %q (the old conversation was s1)", rc.ExternalID, tc.want)
			}
		})
	}
	// A prompt that follows no reset keeps the conversation it was typed into.
	p := newPane("idle")
	rc, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "builder"))
	if err != nil || rc.ExternalID != "s1" {
		t.Errorf("a review prompt: %+v %v", rc, err)
	}
	// A conversation that did not exist before the reset has no stale id to avoid.
	q := newPane("idle")
	q.session, q.resetTok, q.newSession = "", "skip:no-session", "s9"
	rc, err = transportFor(q).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if err != nil || rc.ExternalID != "s9" {
		t.Errorf("a first conversation: %+v %v", rc, err)
	}
	// A first conversation's id can arrive late, or not in time. The receipt
	// names it or nothing, never the pane, whose id has no log.
	for name, tc := range map[string]struct {
		lag  int
		want string
	}{"the id arrives late": {22, "s9"}, "the id never arrives": {-1, ""}} {
		q := newPane("idle")
		q.session, q.resetTok, q.newSession, q.lag = "", "skip:no-session", "s9", tc.lag
		rc, err := transportFor(q).Send(context.Background(), delivery("builder_dispatch", "builder"))
		if err != nil || rc.ExternalID != tc.want {
			t.Errorf("a first conversation, %s: external id %q, want %q (%v)", name, rc.ExternalID, tc.want, err)
		}
	}
}

// A Copilot pane as Herdr shows it: its session is the id the pane was
// launched with, kept after every /new. The reset names the new conversation
// from Copilot's own log instead.
func copilotPane() *fakePane {
	p := newPane("idle")
	p.kind, p.session, p.resetSession = "copilot", "launch-id", "c2"
	return p
}

func copilotTransport(p *fakePane, share bool) *herdrTransport {
	return newHerdrTransport(transportSpec{Kind: "copilot", Builder: "builder", Planner: "planner", Root: "/repo", ShareContext: share}, p.deps())
}

// The task's receipt names the conversation the reset proved, at once. It
// never names Herdr's id, which is the conversation the pane left.
func TestACopilotTaskReceiptNamesTheConversationItsResetProved(t *testing.T) {
	p := copilotPane()
	start := p.now
	rc, err := copilotTransport(p, false).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if err != nil || rc.ExternalID != "c2" {
		t.Fatalf("receipt %+v, %v; want the reset's conversation c2", rc, err)
	}
	// The turn is watched for failWithin; waiting sessionWithin on top of it
	// is waiting for Herdr to report a session it never reports for Copilot.
	if waited := p.now.Sub(start); waited >= failWithin+sessionWithin {
		t.Errorf("waited %v, past the turn's watch, for a session Herdr never reports for Copilot", waited)
	}
}

// Without a reset, a Copilot receipt cannot name its conversation: Herdr's id
// may be one the pane left at an earlier /new.
func TestACopilotReceiptNeverNamesHerdrsLaunchID(t *testing.T) {
	p := copilotPane()
	p.resetTok, p.resetSession = "skip:holds-build", ""
	rc, err := copilotTransport(p, true).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if err != nil || rc.ExternalID == "launch-id" {
		t.Fatalf("receipt %+v, %v: Herdr's launch id is not the conversation", rc, err)
	}
	q := copilotPane()
	rc, err = copilotTransport(q, false).Send(context.Background(), delivery("review_prompt", "builder"))
	if err != nil || rc.ExternalID == "launch-id" {
		t.Fatalf("review receipt %+v, %v: Herdr's launch id is not the conversation", rc, err)
	}
	obs, err := copilotTransport(copilotPane(), false).Observe(context.Background(), "")
	if err != nil || obs.Session != "" {
		t.Errorf("observation %+v, %v: a Copilot session is never taken from Herdr", obs, err)
	}
}

// A turn that ends within failWithin counts if its conversation used tokens.
// For Copilot that is the conversation the reset proved: Herdr's id names the
// one the pane left, whose log never grows, so reading it would call every
// short task a failed turn.
func TestACopilotTurnIsJudgedByTheConversationItsResetProved(t *testing.T) {
	p := copilotPane()
	p.endAfter, p.promptTokens = 2, 500
	p.tokenLog = map[string]int64{"launch-id": 9000}
	if _, err := copilotTransport(p, false).Send(context.Background(), delivery("builder_dispatch", "builder")); err != nil {
		t.Fatalf("a short task that used tokens in c2 was refused: %v", err)
	}
	q := copilotPane()
	q.endAfter, q.promptTokens = 2, 0
	q.tokenLog = map[string]int64{"launch-id": 9000, "c2": 0}
	if _, err := copilotTransport(q, false).Send(context.Background(), delivery("builder_dispatch", "builder")); !runner.IsNotSent(err) {
		t.Errorf("a turn that used no tokens in c2 counted as started: %v", err)
	}
}

// A Codex pane as Herdr shows it: the session it reports, if any, is
// whatever conversation Codex's shared daemon last credited to this pane,
// which may be another pane's.
func codexPane() *fakePane {
	p := newPane("idle")
	p.kind, p.resetTok, p.newSession = "codex", "sent", "another-panes"
	return p
}

func codexTransport(p *fakePane) *herdrTransport {
	return newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: "/repo"}, p.deps())
}

// A Codex task's receipt names no conversation: none Herdr shows is known to
// be this pane's. The task's conversation is found later by its nonce in
// Codex's own logs (controller.recordBuilderSession). Nor does the send wait
// for an id Herdr will not give.
func TestACodexReceiptNamesNoConversation(t *testing.T) {
	p := codexPane()
	start := p.now
	rc, err := codexTransport(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if err != nil || rc.ExternalID != "" {
		t.Fatalf("receipt %+v, %v; want no conversation named", rc, err)
	}
	if waited := p.now.Sub(start); waited >= failWithin+sessionWithin {
		t.Errorf("waited %v for a Codex session Herdr does not give", waited)
	}
	obs, err := codexTransport(codexPane()).Observe(context.Background(), "")
	if err != nil || obs.Session != "" {
		t.Errorf("observation %+v, %v: a Codex session is never taken from Herdr", obs, err)
	}
}

// A Codex turn that ends within failWithin counts only if its conversation,
// found by the task's nonce, used tokens.
func TestACodexTurnThatEndsAtOnceIsJudgedByItsNonce(t *testing.T) {
	p := codexPane()
	p.logged, p.endAfter, p.paneText = "c9", 3, modelError
	p.tokenLog = map[string]int64{"c9": 0}
	if _, err := codexTransport(p).Send(context.Background(), delivery("builder_dispatch", "builder")); !runner.IsNotSent(err) {
		t.Fatalf("a Codex turn that used no tokens counted as started: %v", err)
	}
	q := codexPane()
	q.logged, q.endAfter, q.promptTokens = "c9", 3, 700
	if _, err := codexTransport(q).Send(context.Background(), delivery("builder_dispatch", "builder")); err != nil {
		t.Fatalf("a short Codex turn that used tokens was refused: %v", err)
	}
}

func TestHerdrSendNeverTypesIntoAnUnconfirmedReset(t *testing.T) {
	for _, tok := range []string{"unconfirmed", "skip:not-idle"} {
		p := newPane("idle")
		p.resetTok = tok
		_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
		if !runner.IsNotSent(err) || len(p.prompts) != 0 {
			t.Errorf("%s: err %v, prompts %q", tok, err, p.prompts)
		}
	}
	// A conversation that does not exist yet, or was just reset, is fresh.
	for _, tok := range []string{"skip:no-session", "sent", "reset"} {
		p := newPane("idle")
		p.resetTok = tok
		if _, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder")); err != nil || len(p.prompts) != 1 {
			t.Errorf("%s: err %v, prompts %q", tok, err, p.prompts)
		}
	}
	// One that cannot be shown to be fresh is not sent a task, unless the run
	// was started accepting the conversation the builder has.
	for _, tok := range []string{"skip:unsupported", "skip:no-identity", "skip:holds-build"} {
		p := newPane("idle")
		p.resetTok = tok
		_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
		if !runner.IsNotSent(err) || len(p.prompts) != 0 || !strings.Contains(err.Error(), "--share-context") {
			t.Errorf("%s: err %v, prompts %q", tok, err, p.prompts)
		}
		q := newPane("idle")
		q.resetTok = tok
		tr := newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: "/repo", ShareContext: true}, q.deps())
		if _, err := tr.Send(context.Background(), delivery("builder_dispatch", "builder")); err != nil || len(q.prompts) != 1 {
			t.Errorf("%s with --share-context: err %v, prompts %q", tok, err, q.prompts)
		}
	}
}

func TestEveryKindIsSentTheSamePromptOrRefusedBeforeTyping(t *testing.T) {
	var typed []string
	for _, kind := range agent.Kinds() {
		a, err := agent.Get(kind)
		if err != nil {
			t.Fatal(err)
		}
		tok := "skip:unsupported"
		if a.ResetCommand() != "" {
			tok = "reset"
		}
		// Strict: only a kind whose reset is verified is sent the task.
		p := newPane("idle")
		p.resetTok = tok
		tr := newHerdrTransport(transportSpec{Kind: kind, Builder: "builder", Root: "/repo"}, p.deps())
		_, err = tr.Send(context.Background(), runner.Delivery{Kind: "builder_dispatch", EffectID: "e1", Nonce: "n1", PromptHash: "h1", AgentKind: kind, SessionID: "builder"})
		if tok == "reset" && (err != nil || len(p.prompts) != 1) {
			t.Errorf("%s has a verified reset and must be sent the task: %v", kind, err)
		}
		if tok != "reset" && (!runner.IsNotSent(err) || len(p.prompts) != 0) {
			t.Errorf("%s has no verified reset and must not be sent a task into an unknown conversation: %v %q", kind, err, p.prompts)
		}
		// Sharing the conversation is explicit, and then every kind gets the same text.
		q := newPane("idle")
		q.resetTok = tok
		tr = newHerdrTransport(transportSpec{Kind: kind, Builder: "builder", Root: "/repo", ShareContext: true}, q.deps())
		if _, err := tr.Send(context.Background(), runner.Delivery{Kind: "builder_dispatch", EffectID: "e1", Nonce: "n1", PromptHash: "h1", AgentKind: kind, SessionID: "builder"}); err != nil {
			t.Errorf("%s with --share-context: %v", kind, err)
		}
		typed = append(typed, strings.Join(q.prompts, "|"))
	}
	if len(typed) < 4 {
		t.Fatalf("only %d kinds registered", len(typed))
	}
	for _, x := range typed[1:] {
		if x != typed[0] || x == "" {
			t.Errorf("the kinds were typed different prompts: %q", typed)
		}
	}
}

func TestHerdrSendRefusesABlockedOrBusyAgentWithoutTyping(t *testing.T) {
	for _, st := range []string{"blocked", "working"} {
		p := newPane(st)
		_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
		if !runner.IsNotSent(err) || len(p.prompts) != 0 || p.resets != 0 {
			t.Errorf("%s: err %v prompts %q resets %d", st, err, p.prompts, p.resets)
		}
	}
	p := newPane("blocked")
	if _, err := transportFor(p).Send(context.Background(), delivery("notify_planner", "planner")); !runner.IsNotSent(err) || len(p.prompts) != 0 {
		t.Errorf("a notice typed into a dialog: %v %q", err, p.prompts)
	}
}

func TestHerdrSendClassifiesHerdrErrors(t *testing.T) {
	declined := &herdr.Error{Code: "agent_not_found", Message: "no agent named builder"}
	noAnswer := &herdr.Error{Code: "unreachable", Message: "Error: Os { code: 1 }", Unreachable: true}

	p := newPane("idle")
	p.getErr = declined
	if _, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder")); !runner.IsNotSent(err) {
		t.Errorf("an agent lookup that Herdr declined must be not-sent: %v", err)
	}
	p = newPane("idle")
	p.getErr = noAnswer
	if _, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder")); !runner.IsNotSent(err) {
		t.Errorf("a lookup types nothing, so even no answer is not-sent: %v", err)
	}
	p = newPane("idle")
	p.promptErr = declined
	if _, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "builder")); !runner.IsNotSent(err) {
		t.Errorf("a prompt Herdr declined is not-sent: %v", err)
	}
	p = newPane("idle")
	p.promptErr = noAnswer
	_, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "builder"))
	if err == nil || runner.IsNotSent(err) {
		t.Errorf("a prompt command that never got an answer may have been typed: %v", err)
	}
	p = newPane("idle")
	p.promptErr = errors.New("signal: killed")
	if _, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "builder")); err == nil || runner.IsNotSent(err) {
		t.Errorf("a killed prompt command is uncertain: %v", err)
	}
}

func TestHerdrSendNeedsTheAgentToLeaveIdleBeforeItCountsAsDelivered(t *testing.T) {
	p := newPane("idle")
	p.swallow = true
	_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if err == nil || runner.IsNotSent(err) {
		t.Fatalf("a prompt that changed nothing Herdr can see is uncertain, not delivered and not not-sent: %v", err)
	}
	if !strings.Contains(err.Error(), "dialog") {
		t.Errorf("the error should point at the pane: %v", err)
	}
	if len(p.prompts) != 1 {
		t.Errorf("typed %d times", len(p.prompts))
	}
	if p.now.Sub(time.Unix(1_700_000_000, 0)) > 20*time.Second {
		t.Errorf("waited %v for the agent to move", p.now.Sub(time.Unix(1_700_000_000, 0)))
	}
	// The same rule covers the review prompt.
	p = newPane("done")
	p.swallow = true
	if _, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "builder")); err == nil || runner.IsNotSent(err) {
		t.Errorf("review prompt: %v", err)
	}
}

func TestHerdrSendByKind(t *testing.T) {
	// A review prompt goes to the same conversation: no reset.
	p := newPane("idle")
	if _, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "builder")); err != nil || p.resets != 0 || len(p.prompts) != 1 {
		t.Errorf("review: err %v resets %d prompts %q", err, p.resets, p.prompts)
	}
	// A notice goes to the named planner, without a reset and without waiting.
	p = newPane("idle")
	p.swallow = true
	if _, err := transportFor(p).Send(context.Background(), delivery("notify_planner", "planner")); err != nil || p.resets != 0 || p.prompts[0] != "planner: PROMPT h1" {
		t.Errorf("notice: err %v resets %d prompts %q", err, p.resets, p.prompts)
	}
	// A cancel reaches a builder that is mid-turn.
	p = newPane("working")
	if _, err := transportFor(p).Send(context.Background(), delivery("cancel", "builder")); err != nil || p.resets != 0 || len(p.prompts) != 1 {
		t.Errorf("cancel: err %v resets %d prompts %q", err, p.resets, p.prompts)
	}
	// An unknown kind is refused before anything is typed.
	p = newPane("idle")
	if _, err := transportFor(p).Send(context.Background(), delivery("check_dispatch", "builder")); !runner.IsNotSent(err) || len(p.prompts) != 0 {
		t.Errorf("an internal effect reached the transport: %v %q", err, p.prompts)
	}
}

// A notice's receipt finishes the run, so a notice is typed whatever the
// planner is doing, short of a dialog: into a running turn it queues, and it
// still gets a receipt.
func TestHerdrSendQueuesANoticeBehindARunningTurn(t *testing.T) {
	p := newPane("working")
	rc, err := transportFor(p).Send(context.Background(), delivery("notify_planner", "planner"))
	if err != nil || rc.EffectID != "e1" || len(p.prompts) != 1 {
		t.Fatalf("receipt %+v err %v prompts %q", rc, err, p.prompts)
	}
}

func TestHerdrSendRefusesWhatItCannotAddress(t *testing.T) {
	p := newPane("idle")
	if _, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "")); !runner.IsNotSent(err) || len(p.prompts) != 0 {
		t.Errorf("no agent name: %v", err)
	}
	d := delivery("builder_dispatch", "builder")
	d.PromptHash = "missing"
	if _, err := transportFor(p).Send(context.Background(), d); !runner.IsNotSent(err) || len(p.prompts) != 0 || p.resets != 0 {
		t.Errorf("a prompt that cannot be read must stop before the reset: %v resets %d", err, p.resets)
	}
	long := newPane("idle")
	deps := long.deps()
	deps.readPrompt = func(context.Context, string) (string, error) { return strings.Repeat("x", maxPromptBytes+1), nil }
	tr := newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Root: "/repo"}, deps)
	if _, err := tr.Send(context.Background(), delivery("builder_dispatch", "builder")); !runner.IsNotSent(err) || len(long.prompts) != 0 {
		t.Errorf("an oversize prompt: %v", err)
	}
}

func TestHerdrObserveReportsOnlyWhatHerdrCanSee(t *testing.T) {
	for status, want := range map[string]string{
		"working": runner.ObsRunning,
		"blocked": runner.ObsRunning,
		"idle":    runner.ObsCompleted,
		"done":    runner.ObsCompleted,
		"unknown": runner.ObsUnknown,
		"":        runner.ObsUnknown,
		"weird":   runner.ObsUnknown,
	} {
		p := newPane(status)
		obs, err := transportFor(p).Observe(context.Background(), "e1")
		if err != nil || obs.State != want {
			t.Errorf("%q: %+v %v, want %s", status, obs, err, want)
		}
		if obs.Nonce != "" {
			t.Errorf("%q: a pane's status cannot show the effect's nonce, got %q", status, obs.Nonce)
		}
	}
	// Session is the conversation the CLI reported, and only that. A pane id names
	// no log.
	p := newPane("idle")
	if obs, _ := transportFor(p).Observe(context.Background(), "e1"); obs.Session != "s1" || obs.ExternalID != "s1" {
		t.Errorf("a pane with a session: %+v", obs)
	}
	p.session = ""
	if obs, _ := transportFor(p).Observe(context.Background(), "e1"); obs.Session != "" || obs.ExternalID != "%1" {
		t.Errorf("a pane with no session yet: %+v", obs)
	}
	p.getErr = &herdr.Error{Code: "agent_not_found", Message: "agent target builder not found"}
	_, err := transportFor(p).Observe(context.Background(), "e1")
	if err == nil {
		t.Error("an agent Herdr cannot find is not a stopped agent")
	}
	// Herdr's own message survives the wrap, for whoever reads the pause.
	if !errors.Is(err, runner.ErrAgentGone) || !strings.Contains(err.Error(), "agent target builder not found") {
		t.Errorf("gone agent: %v, want ErrAgentGone carrying Herdr's message", err)
	}
}

func TestDefaultTransportServesEveryRegisteredKindAndNothingElse(t *testing.T) {
	spec := transportSpec{Builder: "builder", Planner: "planner", Root: t.TempDir(), Reviewer: "reviewer", ReviewerKind: "claude"}
	for _, kind := range agent.Kinds() {
		spec.Kind = kind
		if tr, err := defaultDurableTransport(spec); err != nil || tr == nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	spec.Kind = "nonesuch"
	if tr, err := defaultDurableTransport(spec); err == nil || tr != nil || !strings.Contains(err.Error(), "nonesuch") {
		t.Errorf("an unregistered kind: %v", err)
	}
	spec.Kind, spec.Builder = "codex", ""
	if _, err := defaultDurableTransport(spec); err == nil {
		t.Error("a transport with no builder name")
	}
}

// A verification is typed into the planner's pane, and observing it looks at
// the planner, not the builder.
func TestHerdrVerifyPromptGoesToThePlannerAndIsObservedThere(t *testing.T) {
	p := newPane("idle")
	tr := transportFor(p)
	if _, err := tr.Send(context.Background(), delivery("verify_prompt", "planner")); err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 1 || p.prompts[0] != "planner: PROMPT h1" {
		t.Fatalf("prompts %q", p.prompts)
	}
	var asked []string
	p = newPane("idle") // typing a prompt makes the fake pane work
	d := p.deps()
	get := d.get
	d.get = func(name string) (*herdr.Agent, error) { asked = append(asked, name); return get(name) }
	tr = newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: "/repo"}, d)
	if obs, err := tr.ObserveAgent(context.Background(), "planner"); err != nil || obs.State != runner.ObsCompleted {
		t.Fatalf("the planner at rest: %+v %v", obs, err)
	}
	if _, err := tr.Observe(context.Background(), "e1"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != "planner" || asked[1] != "builder" {
		t.Fatalf("observed %q, want the planner then the builder", asked)
	}
}

// A notice waits on nothing, so it needs no event stream.
func TestHerdrSendOfANoticeNeedsNoWatch(t *testing.T) {
	p := newPane("idle")
	d := p.deps()
	d.watch = func(...string) (eventWatch, error) {
		return nil, &herdr.Error{Code: "unreachable", Unreachable: true}
	}
	tr := newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: "/repo"}, d)
	if _, err := tr.Send(context.Background(), delivery("notify_planner", "planner")); err != nil || len(p.prompts) != 1 {
		t.Fatalf("err %v prompts %q", err, p.prompts)
	}
}

func TestAwaitSessionNeverReturnsTheOldSession(t *testing.T) {
	p := newPane("idle")
	p.session = "old"
	d := p.deps()
	w, _ := d.watch("builder")
	if s := awaitSession(d, w, "builder", "old", 3*time.Second); s != "" {
		t.Fatalf("got %q, want \"\"", s)
	}
}

func TestAwaitSessionReturnsTheNewSessionFromAnEvent(t *testing.T) {
	p := newPane("idle")
	p.session, p.newSession, p.lag, p.resets = "old", "new", 2, 1
	d := p.deps()
	w, _ := d.watch("builder")
	if s := awaitSession(d, w, "builder", "old", 6*time.Second); s != "new" {
		t.Fatalf("got %q, want new", s)
	}
}
