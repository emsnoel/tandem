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
// pane's answer to the key. screen is what the pane shows from the reset
// command until a key is pressed, and "" otherwise.
type fakeHerdr struct {
	states    []*herdr.Agent
	afterKeys []*herdr.Agent
	screen    string
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
	return f.depsFrom(func(string) string { return cmd })
}

// registryDeps answers the reset command from the real agent registry, so a
// test sees what would actually be typed for a kind.
func (f *fakeHerdr) registryDeps() resetDeps {
	return f.depsFrom(liveResetDeps.command)
}

func (f *fakeHerdr) depsFrom(command func(kind string) string) resetDeps {
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
		screen: func(string) (string, error) {
			if len(f.submitted) == 0 || len(f.keys) > 0 {
				return "", nil
			}
			return f.screen, nil
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

// Claude's shape: the id moves as soon as the reset lands.
func TestResetSucceedsWhenTheSessionChanges(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{kindAgent("claude", "idle", "s1", 0), kindAgent("claude", "idle", "s2", 0)}}
	tok, _, _, err := runReset(f.deps("/clear"), "builder", rootWith(t, claudeBuilder, ""), 15*time.Second, false)
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
	tok, _, _, _ := runReset(f.deps("/clear"), "builder", rootWith(t, claudeBuilder, ""), 15*time.Second, false)
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
	tok, _, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
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
// The screen shows it: Enter picks its first row, the current checkout, and
// the reset then waits for the new conversation.
func TestResetAnswersTheNewConversationPicker(t *testing.T) {
	f := &fakeHerdr{
		screen:    checkoutPicker(t, ""),
		states:    []*herdr.Agent{mkAgentAt("idle", "s1", 5)},
		afterKeys: []*herdr.Agent{mkAgentAt("working", "s1", 6), mkAgentAt("idle", "s1", 7)},
	}
	tok, _, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
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

// Nothing ever moves and the screen shows no dialog the reset knows: whatever
// the pane is showing, it is not a new conversation, and no key goes into it.
func TestResetUnconfirmedWhenNothingEverMoves(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("idle", "s1", 5)}}
	tok, _, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "unconfirmed" {
		t.Fatalf("got %q, want unconfirmed", tok)
	}
	if len(f.keys) != 0 {
		t.Errorf("pressed %v into a screen the reset does not know", f.keys)
	}
}

// Codex's picker is answered for a Codex pane only.
func TestResetPressesNothingIntoAnotherKindsPane(t *testing.T) {
	f := &fakeHerdr{screen: checkoutPicker(t, ""), states: []*herdr.Agent{kindAgent("claude", "idle", "s1", 5)}}
	tok, _, _, _ := runReset(f.deps("/clear"), "builder", rootWith(t, claudeBuilder, ""), 15*time.Second, false)
	if tok != "unconfirmed" || len(f.keys) != 0 {
		t.Fatalf("got %q, pressed %v; want unconfirmed and no key", tok, f.keys)
	}
}

// A finished turn nobody looked at reads done, which is settled, not busy.
func TestResetFromAFinishedUnseenBuilder(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{
		mkAgentAt("done", "s1", 5), mkAgentAt("working", "s1", 6), mkAgentAt("done", "s1", 7),
	}}
	tok, _, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
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
	tok, _, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), 5*time.Second, false)
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
		tok, _, _, err := runReset(f.deps(c.cmd), "builder", rootWith(t, c.builder, c.last), time.Second, false)
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
	tok, _, _, _ := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), time.Second, true)
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
