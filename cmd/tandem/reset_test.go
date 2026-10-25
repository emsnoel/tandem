package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/paths"
)

func mkAgent(status, session string) *herdr.Agent {
	return mkAgentAt(status, session, 0)
}

// mkAgentAt also sets state_change_seq, which Herdr bumps on every lifecycle
// change.
func mkAgentAt(status, session string, seq int) *herdr.Agent {
	a := &herdr.Agent{PaneID: "w1:p2", Status: &status, StateChangeSeq: seq}
	if session != "" {
		a.Session = &herdr.AgentSession{Value: session}
	}
	return a
}

// kindAgent is mkAgentAt for an agent Herdr detected as kind. What its
// session means depends on the kind (herdr.Agent.Conversation).
func kindAgent(kind, status, session string, seq int) *herdr.Agent {
	a := mkAgentAt(status, session, seq)
	a.Kind = kind
	return a
}

// fakeHerdr serves a scripted sequence of agent states, one per get, repeating
// the last; it records what was submitted and advances a fake clock on sleep.
// Once a key is pressed it serves afterKeys instead, when that is set: the
// pane's answer to the key.
type fakeHerdr struct {
	states    []*herdr.Agent
	afterKeys []*herdr.Agent
	gets      int
	keyGets   int
	submitted []string
	keys      []string
	kinds     []string // every kind the reset command was looked up for
	clock     time.Time
	// fresh answers the kind's own record of a new conversation, one answer
	// per call, repeating the last. nil is a kind with no such record.
	fresh      []string
	freshCalls int
}

func (f *fakeHerdr) deps(cmd string) resetDeps {
	return f.depsWith(cmd, "")
}

func (f *fakeHerdr) depsWith(cmd, confirm string) resetDeps {
	return f.depsFrom(func(string) string { return cmd }, func(string) string { return confirm })
}

// registryDeps answers the reset command and confirm key from the real agent
// registry, so a test sees what would actually be typed for a kind.
func (f *fakeHerdr) registryDeps() resetDeps {
	return f.depsFrom(liveResetDeps.command, liveResetDeps.confirm)
}

func (f *fakeHerdr) depsFrom(command, confirm func(kind string) string) resetDeps {
	// read is what Herdr shows next. The watch takes one entry per step, and
	// get (the loop's closing read after an event or wake) shows what the
	// watch last saw, so a state list is Herdr over time and a step reads it
	// once.
	var seen *herdr.Agent
	read := func(string) (*herdr.Agent, error) {
		seq, i := f.states, &f.gets
		if len(f.keys) > 0 && f.afterKeys != nil {
			seq, i = f.afterKeys, &f.keyGets
		}
		n := *i
		if n >= len(seq) {
			n = len(seq) - 1
		}
		*i++
		seen = seq[n]
		return seen, nil
	}
	get := func(string) (*herdr.Agent, error) { return seen, nil }
	return resetDeps{
		command: func(kind string) string { f.kinds = append(f.kinds, kind); return command(kind) },
		confirm: confirm,
		get:     get,
		watch: func(names ...string) (eventWatch, error) {
			return newStepWatch(names[0], read, func(d time.Duration) { f.clock = f.clock.Add(d) })
		},
		submit: func(_ string, text string) error {
			f.submitted = append(f.submitted, text)
			return nil
		},
		press: func(_ string, key string) error {
			f.keys = append(f.keys, key)
			return nil
		},
		fresh: func(string, *herdr.Agent, time.Time) (string, bool) {
			if f.fresh == nil {
				return "", false
			}
			n := min(f.freshCalls, len(f.fresh)-1)
			f.freshCalls++
			return f.fresh[n], true
		},
		now: func() time.Time { return f.clock },
	}
}

func rootWith(t *testing.T, builderJSON, buildSession string) string {
	t.Helper()
	root := t.TempDir()
	mb := paths.Relay(root)
	if err := os.MkdirAll(mb, 0o755); err != nil {
		t.Fatal(err)
	}
	if builderJSON != "" {
		if err := os.WriteFile(filepath.Join(mb, "builder.json"), []byte(builderJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if buildSession != "" {
		if err := os.WriteFile(filepath.Join(mb, "build-session"), []byte(buildSession+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const (
	codexBuilder   = `{"kind":"codex","autonomy":"auto"}`
	claudeBuilder  = `{"kind":"claude","autonomy":"auto"}`
	copilotBuilder = `{"kind":"copilot","autonomy":"auto"}`
)

// withReviewer writes the reviewer.json tandem-pair --reviewer records.
func withReviewer(t *testing.T, root, reviewerJSON string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(paths.Relay(root), "reviewer.json"), []byte(reviewerJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A Claude builder with a Codex reviewer: the reviewer must be reset as Codex
// (/new plus the picker's Enter), not with the builder's /clear.
func TestResetUsesTheReviewersRecordedKind(t *testing.T) {
	root := withReviewer(t, rootWith(t, `{"kind":"claude","autonomy":"auto"}`, ""), `{"kind":"codex","name":"reviewer"}`)
	f := &fakeHerdr{
		states:    []*herdr.Agent{mkAgentAt("idle", "s1", 5)},
		afterKeys: []*herdr.Agent{mkAgentAt("working", "s1", 6), mkAgentAt("idle", "s1", 7)},
	}
	tok, _, err := runReset(f.registryDeps(), "reviewer", root, 15*time.Second, false)
	if err != nil || tok != "sent" {
		t.Fatalf("got %q, %v; want sent", tok, err)
	}
	if len(f.kinds) != 1 || f.kinds[0] != "codex" {
		t.Errorf("looked up kinds %v, want [codex]", f.kinds)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/new" {
		t.Errorf("submitted %v, want [/new]", f.submitted)
	}
	if len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Errorf("pressed %v, want [enter]", f.keys)
	}
}

// A reviewer being recorded changes nothing for any other pane.
func TestResetUsesTheBuilderKindForOtherNames(t *testing.T) {
	root := withReviewer(t, rootWith(t, `{"kind":"claude","autonomy":"auto"}`, ""), `{"kind":"codex","name":"reviewer"}`)
	f := &fakeHerdr{states: []*herdr.Agent{kindAgent("claude", "idle", "s1", 0), kindAgent("claude", "idle", "s2", 0)}}
	tok, _, err := runReset(f.registryDeps(), "builder", root, 15*time.Second, false)
	if err != nil || tok != "reset" {
		t.Fatalf("got %q, %v; want reset", tok, err)
	}
	if len(f.kinds) != 1 || f.kinds[0] != "claude" {
		t.Errorf("looked up kinds %v, want [claude]", f.kinds)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/clear" {
		t.Errorf("submitted %v, want [/clear]", f.submitted)
	}
}

// Without reviewer.json every name is reset by the builder's kind.
func TestResetWithoutAReviewerUsesTheBuilderKind(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgent("idle", "s1"), mkAgent("idle", "s2")}}
	if _, _, err := runReset(f.registryDeps(), "reviewer", rootWith(t, codexBuilder, ""), 15*time.Second, false); err != nil {
		t.Fatal(err)
	}
	if len(f.kinds) != 1 || f.kinds[0] != "codex" {
		t.Errorf("looked up kinds %v, want [codex]", f.kinds)
	}
}

// A broken reviewer.json means reset cannot tell whether NAME is the reviewer,
// so every pane is refused until the pair is repaired.
func TestResetRefusesAnUnreadableReviewerRecord(t *testing.T) {
	for _, c := range []struct{ label, rec, name string }{
		{"not json, the builder", `not json`, "builder"},
		{"not json, the reviewer", `not json`, "reviewer"},
		{"no kind", `{"kind":"","name":"reviewer"}`, "reviewer"},
		{"no name", `{"kind":"codex"}`, "builder"},
	} {
		root := withReviewer(t, rootWith(t, codexBuilder, ""), c.rec)
		f := &fakeHerdr{states: []*herdr.Agent{mkAgent("idle", "s1")}}
		tok, _, err := runReset(f.registryDeps(), c.name, root, time.Second, false)
		if err != nil || tok != "skip:no-identity" {
			t.Errorf("%s: got %q, %v; want skip:no-identity", c.label, tok, err)
		}
		if len(f.submitted) != 0 {
			t.Errorf("%s: submitted %v; a refusal must send nothing", c.label, f.submitted)
		}
	}
}

// A reviewer of a kind the registry does not know has no measured reset.
func TestResetSkipsAReviewerOfAnUnknownKind(t *testing.T) {
	root := withReviewer(t, rootWith(t, codexBuilder, ""), `{"kind":"nonesuch","name":"reviewer"}`)
	f := &fakeHerdr{states: []*herdr.Agent{mkAgent("idle", "s1")}}
	tok, _, _ := runReset(f.registryDeps(), "reviewer", root, time.Second, false)
	if tok != "skip:unsupported" || len(f.submitted) != 0 {
		t.Fatalf("got %q, submitted %v; want skip:unsupported and nothing sent", tok, f.submitted)
	}
}

// Claude's shape: the id moves as soon as the reset lands.
func TestResetSucceedsWhenTheSessionChanges(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{kindAgent("claude", "idle", "s1", 0), kindAgent("claude", "idle", "s2", 0)}}
	tok, _, err := runReset(f.deps("/clear"), "builder", rootWith(t, claudeBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "reset" {
		t.Fatalf("got %q, %v; want reset", tok, err)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/clear" {
		t.Errorf("submitted %v, want [/clear]", f.submitted)
	}
}

// The launch prompt must not be typed while the reset turn is still settling.
func TestResetWaitsForIdleAfterTheSessionChanges(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{
		kindAgent("claude", "idle", "s1", 0), kindAgent("claude", "working", "s2", 0), kindAgent("claude", "working", "s2", 0), kindAgent("claude", "idle", "s2", 0),
	}}
	tok, _, _ := runReset(f.deps("/clear"), "builder", rootWith(t, claudeBuilder, ""), 15*time.Second, false)
	if tok != "reset" || f.gets != 4 {
		t.Fatalf("got %q after %d gets; want reset after 4", tok, f.gets)
	}
}

// Codex: /new empties the conversation but the id moves only when the next
// turn starts. A lifecycle change followed by settled, with the id unchanged,
// is `sent`. It returns as soon as that is seen and presses nothing.
func TestResetSentWhenTheSessionMovesOnlyOnTheNextTurn(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{
		mkAgentAt("idle", "s1", 1), mkAgentAt("working", "s1", 2), mkAgentAt("idle", "s1", 3),
	}}
	start := f.clock
	tok, _, _ := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "sent" {
		t.Fatalf("got %q, want sent", tok)
	}
	if waited := f.clock.Sub(start); waited > 2*resetPoll {
		t.Errorf("waited %v; want to return once the new conversation settled", waited)
	}
	if len(f.keys) != 0 {
		t.Errorf("pressed %v; a reset that ran needs no confirmation", f.keys)
	}
}

// Codex's /new opens a picker Herdr cannot see, and Herdr keeps reporting idle.
// Nothing moving is not a reset: press the confirm key once (Enter picks
// "Current checkout"), then wait for the new conversation.
func TestResetAnswersTheNewConversationPicker(t *testing.T) {
	f := &fakeHerdr{
		states:    []*herdr.Agent{mkAgentAt("idle", "s1", 5)},
		afterKeys: []*herdr.Agent{mkAgentAt("working", "s1", 6), mkAgentAt("idle", "s1", 7)},
	}
	tok, _, _ := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "sent" {
		t.Fatalf("got %q, want sent", tok)
	}
	if len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Errorf("pressed %v, want [enter]", f.keys)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/new" {
		t.Errorf("submitted %v, want [/new]", f.submitted)
	}
}

// Nothing ever moves, even after the confirm key: whatever the pane is
// showing, it is not a new conversation. The key is pressed once, not
// repeated into an unknown screen.
func TestResetUnconfirmedWhenNothingEverMoves(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("idle", "s1", 5)}}
	tok, _, _ := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "unconfirmed" {
		t.Fatalf("got %q, want unconfirmed", tok)
	}
	if len(f.keys) != 1 {
		t.Errorf("pressed %v; want exactly one confirm key", f.keys)
	}
}

// A kind with no measured confirm key never gets a key pressed.
func TestResetPressesNothingWithoutAConfirmKey(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("idle", "s1", 5)}}
	tok, _, _ := runReset(f.deps("/clear"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "unconfirmed" || len(f.keys) != 0 {
		t.Fatalf("got %q, pressed %v; want unconfirmed and no key", tok, f.keys)
	}
}

// A finished turn nobody looked at reads done, which is settled, not busy.
func TestResetFromAFinishedUnseenBuilder(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{
		mkAgentAt("done", "s1", 5), mkAgentAt("working", "s1", 6), mkAgentAt("done", "s1", 7),
	}}
	tok, _, _ := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "sent" {
		t.Fatalf("got %q, want sent", tok)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/new" {
		t.Errorf("submitted %v, want [/new]", f.submitted)
	}
}

// Never idle again: the reset turn is stuck, so report unconfirmed. It is
// moving, so it is not the picker, and no key is pressed.
func TestResetUnconfirmedWhenTheBuilderNeverSettles(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("idle", "s1", 1), mkAgentAt("working", "s1", 2)}}
	tok, _, _ := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 5*time.Second, false)
	if tok != "unconfirmed" {
		t.Fatalf("got %q, want unconfirmed", tok)
	}
	if len(f.keys) != 0 {
		t.Errorf("pressed %v into a busy builder", f.keys)
	}
}

func TestResetSkips(t *testing.T) {
	for _, c := range []struct {
		name, builder, last, cmd string
		agent                    *herdr.Agent
		want                     string
	}{
		{"no identity recorded", "", "", "/new", mkAgent("idle", "s1"), "skip:no-identity"},
		{"no verified command", codexBuilder, "", "", mkAgent("idle", "s1"), "skip:unsupported"},
		{"not idle", codexBuilder, "", "/new", mkAgent("working", "s1"), "skip:not-idle"},
		{"known to have no conversation", claudeBuilder, "", "/clear", kindAgent("claude", "idle", "", 0), "skip:no-session"},
		{"already holds a build", claudeBuilder, "s1", "/clear", kindAgent("claude", "idle", "s1", 0), "skip:holds-build"},
	} {
		f := &fakeHerdr{states: []*herdr.Agent{c.agent}}
		tok, _, err := runReset(f.deps(c.cmd), "builder", rootWith(t, c.builder, c.last), time.Second, false)
		if err != nil || tok != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, tok, err, c.want)
		}
		if len(f.submitted) != 0 {
			t.Errorf("%s: submitted %v; a skip must send nothing", c.name, f.submitted)
		}
	}
}

func TestResetDryRunSendsNothing(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgent("idle", "s1")}}
	tok, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), time.Second, true)
	if tok != "would-reset" || len(f.submitted) != 0 {
		t.Fatalf("got %q, submitted %v; want would-reset and nothing sent", tok, f.submitted)
	}
}

// --who names the pane that was reset. Only the two roles are accepted.
func TestExplainResetNamesWhoWasReset(t *testing.T) {
	for _, c := range []struct {
		args   []string
		want   string
		wantRC int
	}{
		{[]string{"skip:not-idle", "--for", "review", "--who", "reviewer"},
			"WARNING the reviewer is still working or at a dialog, so its conversation was not reset.\n", 0},
		{[]string{"skip:not-idle", "--for", "build"},
			"WARNING the builder is still working or at a dialog, so its conversation was not reset.\n", 0},
		{[]string{"skip:not-idle", "--who", "planner"}, "", 2},
	} {
		got, rc := captureStdoutRC(t, func() int { return cmdAgentExplainReset(c.args) })
		if rc != c.wantRC || got != c.want {
			t.Errorf("explain-reset %v: rc %d, %q; want rc %d, %q", c.args, rc, got, c.wantRC, c.want)
		}
	}
}

// After delivery, handoff asks for a session other than the pre-reset one, so
// a Codex id that moves into the first turn is the one recorded.
func TestSessionWaitsForADifferentID(t *testing.T) {
	p := newPane("working")
	p.session, p.newSession, p.lag, p.resets = "s1", "s2", 2, 1
	got, err := runSession(p.deps(), "builder", "s1", 5*time.Second)
	if err != nil || got != "s2" {
		t.Fatalf("got %q, %v; want s2", got, err)
	}
}

// If it never moves, report what is there; the caller decides what that means.
func TestSessionReturnsTheCurrentIDOnTimeout(t *testing.T) {
	p := newPane("working")
	got, _ := runSession(p.deps(), "builder", "s1", 2*time.Second)
	if got != "s1" {
		t.Fatalf("got %q, want s1", got)
	}
}

func TestSessionWithoutNotAnswersAtOnce(t *testing.T) {
	p := newPane("idle")
	got, _ := runSession(p.deps(), "builder", "", 5*time.Second)
	if got != "s1" || p.reads != 1 {
		t.Fatalf("got %q after %d reads; want s1 after 1", got, p.reads)
	}
}

// Copilot: /new opens a new conversation, but Herdr keeps the launch id and
// its counter does not move. The process log confirms the reset.
func TestCopilotResetIsConfirmedByItsOwnRecord(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("idle", "launch-id", 5)}, fresh: []string{"", "", "c2"}}
	tok, session, err := runReset(f.registryDeps(), "builder", rootWith(t, copilotBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "reset" || session != "c2" {
		t.Fatalf("got %q, %q, %v; want reset into c2", tok, session, err)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/new" {
		t.Errorf("submitted %v, want [/new]", f.submitted)
	}
}

// For a kind with its own record, only that record proves a reset. Herdr's
// counter also moves when a finished turn is first seen (done to idle), so a
// counter that moved while the record shows nothing is not a reset.
func TestCopilotResetIgnoresHerdrsCounter(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("done", "launch-id", 5), mkAgentAt("idle", "launch-id", 6)}, fresh: []string{""}}
	tok, session, _ := runReset(f.registryDeps(), "builder", rootWith(t, copilotBuilder, ""), 5*time.Second, false)
	if tok != "unconfirmed" || session != "" {
		t.Fatalf("got %q, %q; want unconfirmed: nothing in Copilot's record showed a new conversation", tok, session)
	}
}

// A reset proven by a moved Herdr id reports that id, so the build's receipt
// can name its conversation without waiting for it.
func TestResetReportsTheConversationHerdrMovedTo(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{kindAgent("claude", "idle", "s1", 0), kindAgent("claude", "idle", "s2", 0)}}
	_, session, _ := runReset(f.deps("/clear"), "builder", rootWith(t, claudeBuilder, ""), 15*time.Second, false)
	if session != "s2" {
		t.Errorf("session %q, want s2", session)
	}
}

// A new Copilot pane has had no turn, so there is nothing to reset.
func TestANewCopilotPaneNeedsNoReset(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{kindAgent("copilot", "idle", "", 0)}}
	tok, _, _ := runReset(f.registryDeps(), "builder", rootWith(t, copilotBuilder, ""), time.Second, false)
	if tok != "skip:no-session" || len(f.submitted) != 0 {
		t.Errorf("got %q, submitted %v; want skip:no-session and nothing typed", tok, f.submitted)
	}
}

// For Copilot, Herdr's id is the launch id kept after /new, so
// `tandem agent session --not` must never report it.
func TestSessionNeverReportsCopilotsLaunchID(t *testing.T) {
	p := newPane("idle")
	p.kind, p.session = "copilot", "launch-id"
	if got, _ := runSession(p.deps(), "builder", "", time.Second); got != "" {
		t.Errorf("got %q; Herdr's Copilot id is not the conversation", got)
	}
}

// Codex's shared daemon credits every conversation to the pane that started
// it, so a Codex pane with no session may hold turns. It is reset like any
// other: /new, Enter at the picker, and the pane's own counter proves it.
func TestACodexPaneWithNoSessionIsReset(t *testing.T) {
	f := &fakeHerdr{
		states:    []*herdr.Agent{kindAgent("codex", "idle", "", 18)},
		afterKeys: []*herdr.Agent{kindAgent("codex", "working", "", 37), kindAgent("codex", "idle", "", 38)},
	}
	tok, session, err := runReset(f.registryDeps(), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "sent" || session != "" {
		t.Fatalf("got %q, %q, %v; want sent, naming no conversation", tok, session, err)
	}
	if len(f.submitted) != 1 || f.submitted[0] != "/new" || len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Errorf("submitted %v, pressed %v; want /new, then enter", f.submitted, f.keys)
	}
}

// The session Herdr shows on a Codex pane can be another pane's, so its change
// proves nothing.
func TestACodexResetIsNotProvenByAHerdrIDChange(t *testing.T) {
	f := &fakeHerdr{
		states:    []*herdr.Agent{kindAgent("codex", "idle", "s1", 5), kindAgent("codex", "idle", "another-panes", 5)},
		afterKeys: []*herdr.Agent{kindAgent("codex", "working", "another-panes", 6), kindAgent("codex", "idle", "another-panes", 7)},
	}
	tok, session, _ := runReset(f.registryDeps(), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "sent" || session != "" {
		t.Fatalf("got %q, %q; want sent, after the picker, naming no conversation", tok, session)
	}
	if len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Errorf("pressed %v; a session-only change is no proof, so the confirm key goes once", f.keys)
	}
}

// A status event after the command proves the reset; the counter is not read.
func TestResetCountsAStatusEventAsSent(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{
		kindAgent("codex", "idle", "s1", 5), kindAgent("codex", "working", "s1", 5), kindAgent("codex", "idle", "s1", 5),
	}}
	tok, _, err := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "sent" {
		t.Fatalf("tok %q err %v, want sent", tok, err)
	}
	if len(f.keys) != 0 {
		t.Errorf("pressed %v after the reset was seen", f.keys)
	}
}

// No event comes after /new opens the picker, so the confirm key is pressed
// once at resetConfirmAfter and the result is unconfirmed.
func TestResetPressesTheConfirmKeyOnceWhenNoEventCame(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{kindAgent("codex", "idle", "s1", 5)}}
	tok, _, _ := runReset(f.depsWith("/new", "enter"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "unconfirmed" || len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Fatalf("tok %q keys %v, want unconfirmed after one enter", tok, f.keys)
	}
}
