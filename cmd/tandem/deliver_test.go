package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// deliverPrompt is driven here through fakePane (build_transport_test.go),
// whose clock moves only when the code under test waits.

func TestDeliverConfirmsATurnThatRuns(t *testing.T) {
	p := newPane("idle")
	res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true})
	if res.outcome != sendStarted || res.agent.StatusString() != "working" {
		t.Fatalf("outcome %v agent %+v err %v", res.outcome, res.agent, res.err)
	}
	if len(p.prompts) != 1 || p.prompts[0] != "builder: hi" {
		t.Errorf("prompts %q", p.prompts)
	}
}

// Where Herdr names no conversation, a prompt with no nonce gets a marker of
// its own, by which its turn is found in the CLI's logs. A nonce the
// text already carries is used as it is.
func TestDeliverMarksAPromptOnlyWhenItCarriesNoNonce(t *testing.T) {
	p := newPane("idle")
	p.kind = "codex"
	deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true})
	if len(p.prompts) != 1 || !strings.Contains(p.prompts[0], "(tandem delivery ") {
		t.Errorf("no marker: %q", p.prompts)
	}
	p = newPane("idle")
	p.kind = "codex"
	deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi", nonce: "n1"}, deliverPolicy{confirm: true})
	if len(p.prompts) != 1 || p.prompts[0] != "builder: hi" {
		t.Errorf("a prompt with a nonce was changed: %q", p.prompts)
	}
}

func TestDeliverRefusesWithoutTyping(t *testing.T) {
	for _, c := range []struct {
		name   string
		pane   func() *fakePane
		policy deliverPolicy
		code   string
	}{
		{"a dialog, no wait", func() *fakePane { return newPane("blocked") }, deliverPolicy{}, "agent_blocked"},
		{"a dialog that stays", func() *fakePane { return newPane("blocked") }, deliverPolicy{confirm: true, wait: 2 * time.Second}, "agent_blocked"},
		{"mid-turn, settled required", func() *fakePane { return newPane("working") }, deliverPolicy{settled: true, confirm: true}, "agent_busy"},
		{"a subagent's screen", func() *fakePane {
			p := newPane("idle")
			p.screens = []string{claudeSubagentScreen}
			return p
		}, deliverPolicy{confirm: true}, "agent_input_elsewhere"},
	} {
		p := c.pane()
		res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, c.policy)
		if res.outcome != sendRefused || res.code != c.code || len(p.prompts) != 0 {
			t.Errorf("%s: outcome %v code %q err %v prompts %q", c.name, res.outcome, res.code, res.err, p.prompts)
		}
	}
}

func TestDeliverWaitsOutADialog(t *testing.T) {
	p := newPane("blocked")
	p.unblockAfter = 2 // the dialog clears on the 2nd read
	res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true, wait: 10 * time.Second})
	if res.outcome != sendStarted || len(p.prompts) != 1 {
		t.Fatalf("outcome %v err %v prompts %q", res.outcome, res.err, p.prompts)
	}
}

// A watch that cannot be reopened after a wait types nothing and says why.
// The watch it replaced is already closed, and nothing closes a nil one.
func TestDeliverStopsWhenTheWatchCannotBeReopened(t *testing.T) {
	p := newPane("blocked")
	p.unblockAfter = 2
	d := p.deps()
	watch, opened := d.watch, 0
	d.watch = func(names ...string) (eventWatch, error) {
		if opened++; opened > 1 {
			return nil, &herdr.Error{Code: "unreachable", Message: "no socket", Unreachable: true}
		}
		return watch(names...)
	}
	res := deliverPrompt(d, deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true, wait: 10 * time.Second})
	if res.outcome != sendRefused || herdr.ExitCodeFor(res.err) != exitcode.Unreachable || len(p.prompts) != 0 {
		t.Fatalf("outcome %v err %v prompts %q", res.outcome, res.err, p.prompts)
	}
}

// A message that is not confirmed needs no event stream: it is typed after
// one read, and typed into a running turn it queues.
func TestDeliverTypesAnUnconfirmedMessageWithoutAWatch(t *testing.T) {
	for st, want := range map[string]deliverOutcome{"idle": sendTyped, "working": sendQueued} {
		p := newPane(st)
		d := p.deps()
		d.watch = func(...string) (eventWatch, error) {
			t.Fatal("opened a watch")
			return nil, nil
		}
		res := deliverPrompt(d, deliverReq{name: "planner", text: "note"}, deliverPolicy{})
		if res.outcome != want || len(p.prompts) != 1 || res.agent == nil {
			t.Errorf("%s: outcome %v err %v prompts %q", st, res.outcome, res.err, p.prompts)
		}
	}
}

// Herdr declining a prompt typed nothing. A command that got no answer, or
// died, may have typed it.
func TestDeliverClassifiesAPromptError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want deliverOutcome
	}{
		{&herdr.Error{Code: "agent_not_found", Message: "no agent named builder"}, sendRefused},
		{&herdr.Error{Code: "unreachable", Message: "no socket", Unreachable: true}, sendUncertain},
		{errors.New("signal: killed"), sendUncertain},
	} {
		p := newPane("idle")
		p.promptErr = c.err
		res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true})
		if res.outcome != c.want || res.err != c.err {
			t.Errorf("%v: outcome %v err %v", c.err, res.outcome, res.err)
		}
	}
}

func TestDeliverResetsBeforeATask(t *testing.T) {
	task := deliverPolicy{settled: true, reset: true, confirm: true}
	p := newPane("idle")
	p.resetTok, p.resetSession = "reset", "c2"
	res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "task", root: "/repo"}, task)
	if res.outcome != sendStarted || !res.reset || res.proved != "c2" || res.before != "s1" || p.resets != 1 {
		t.Fatalf("outcome %v reset %v proved %q before %q resets %d", res.outcome, res.reset, res.proved, res.before, p.resets)
	}
	for _, c := range []struct {
		tok   string
		share bool
		want  deliverOutcome
		code  string
	}{
		{"skip:unsupported", false, sendRefused, "reset_unverified"},
		{"skip:unsupported", true, sendStarted, ""},
		{"unconfirmed", true, sendRefused, "reset_failed"},
		{"skip:off-main", false, sendRefused, "reset_failed"},
	} {
		p := newPane("idle")
		p.resetTok = c.tok
		res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "task", root: "/repo", shareContext: c.share}, task)
		if res.outcome != c.want || res.code != c.code || res.reset {
			t.Errorf("%s share=%v: outcome %v code %q reset %v err %v", c.tok, c.share, res.outcome, res.code, res.reset, res.err)
		}
	}
}

func TestDeliverReportsATurnThatDidNoWork(t *testing.T) {
	p := newPane("idle")
	p.endAfter, p.paneText = 1, "API Error: model not found"
	res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true})
	if res.outcome != sendEndedAtOnce || res.paneText != p.paneText {
		t.Errorf("ended at once: outcome %v pane %q", res.outcome, res.paneText)
	}
	p = newPane("idle")
	p.swallow = true
	if res := deliverPrompt(p.deps(), deliverReq{name: "builder", text: "hi"}, deliverPolicy{confirm: true}); res.outcome != sendNeverStarted {
		t.Errorf("swallowed: outcome %v err %v", res.outcome, res.err)
	}
}
