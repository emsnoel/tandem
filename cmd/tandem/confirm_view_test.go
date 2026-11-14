package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

// Claude's screens, cut down from internal/agent/testdata/screens: a
// subagent's titles the rule above its input, the main conversation's does not.
const (
	claudeSubagentScreen = "──────── Write the story ─\n❯ Message @general-purpose…\n────────────────\n"
	claudeMainScreen     = "────────────────\n❯ \n────────────────\n"
)

// The planner pane showed a background subagent's screen and Herdr said
// idle. A prompt typed there went to the subagent, so nothing is typed.
func TestPromptAsTypesNothingIntoASubagentScreen(t *testing.T) {
	p := newPane("idle")
	p.screens = []string{claudeSubagentScreen}
	var errb bytes.Buffer
	code := promptAs(p.deps(), "planner", "verdict", deliverPolicy{confirm: true}, &errb)
	if code != exitcode.HerdrError || !strings.HasPrefix(errb.String(), "agent_input_elsewhere\t") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "Write the story") || !strings.Contains(errb.String(), "nothing was typed") {
		t.Errorf("stderr %q should name the subagent and say nothing was typed", errb.String())
	}
	if len(p.prompts) != 0 {
		t.Errorf("typed %v into a subagent's screen", p.prompts)
	}
}

// Given time to wait, as for a dialog, a pane that returns to its main
// conversation gets the prompt.
func TestPromptAsWaitsForTheMainScreen(t *testing.T) {
	p := newPane("idle")
	p.screens = []string{claudeSubagentScreen, claudeSubagentScreen, claudeMainScreen}
	start := p.now
	code := promptAs(p.deps(), "planner", "verdict", deliverPolicy{confirm: true, wait: time.Minute}, io.Discard)
	if code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	if len(p.prompts) != 1 {
		t.Fatalf("prompts %v, want one", p.prompts)
	}
	if waited := p.now.Sub(start); waited >= time.Minute {
		t.Errorf("waited %v; the screen was back long before the deadline", waited)
	}
}

func TestPromptAsGivesUpOnAScreenThatStaysOff(t *testing.T) {
	p := newPane("idle")
	p.screens = []string{claudeSubagentScreen}
	start := p.now
	var errb bytes.Buffer
	code := promptAs(p.deps(), "planner", "verdict", deliverPolicy{confirm: true, wait: 10 * time.Second}, &errb)
	if code != exitcode.HerdrError || !strings.HasPrefix(errb.String(), "agent_input_elsewhere\t") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if len(p.prompts) != 0 {
		t.Errorf("typed %v", p.prompts)
	}
	if waited := p.now.Sub(start); waited < 10*time.Second {
		t.Errorf("gave up after %v, before the 10s it was given", waited)
	}
}

// The screen is read for the pane's own kind: a Claude screen in a Codex pane
// is not Codex's sub-agent view, and a screen that cannot be read never holds
// a prompt back.
func TestPromptAsReadsTheScreenForThePanesKind(t *testing.T) {
	p := newPane("idle")
	p.kind = "codex"
	p.screens = []string{claudeSubagentScreen}
	if code := promptAs(p.deps(), "builder", "hi", deliverPolicy{confirm: true}, io.Discard); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	p = newPane("idle")
	p.screenErr = &herdr.Error{Code: "unreachable", Unreachable: true}
	if code := promptAs(p.deps(), "builder", "hi", deliverPolicy{confirm: true}, io.Discard); code != exitcode.OK {
		t.Fatalf("exit %d with an unreadable screen", code)
	}
}

// The run types tasks and reviews into builder and reviewer panes the
// same way. A pane off its main conversation is not sent to, and not reset:
// the reset command would be typed into the same screen.
func TestHerdrSendTypesNothingIntoASubagentScreen(t *testing.T) {
	p := newPane("idle")
	p.kind = "claude"
	p.screens = []string{claudeSubagentScreen}
	_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if !runner.IsNotSent(err) {
		t.Fatalf("err %v, want NotSent", err)
	}
	if !strings.Contains(err.Error(), "Write the story") {
		t.Errorf("err %q should name what the pane shows", err)
	}
	if len(p.prompts) != 0 || p.resets != 0 {
		t.Errorf("prompts %v, resets %d; want neither", p.prompts, p.resets)
	}
}

func TestResetSkipsAPaneOffItsMainScreen(t *testing.T) {
	root := rootWith(t, `{"kind":"claude","autonomy":"auto"}`, "")
	f := &fakeHerdr{states: []*herdr.Agent{mkAgentAt("idle", "s1", 5)}}
	d := f.registryDeps()
	d.screen = func(string) (string, error) { return claudeSubagentScreen, nil }
	tok, _, err := runReset(d, "builder", root, 15*time.Second, false)
	if err != nil || tok != "skip:off-main" {
		t.Fatalf("got %q, %v; want skip:off-main", tok, err)
	}
	if len(f.submitted) != 0 {
		t.Errorf("submitted %v into a subagent's screen", f.submitted)
	}
}

// Every kind tandem can pair has a screen that hands typed text to a child or
// to a list instead of its conversation (agent.OffMainInput); none of them is
// typed into, whichever role the pane has.
func TestPromptAsTypesNothingIntoAnyKindsChildScreen(t *testing.T) {
	for kind, screen := range map[string]string{
		"claude":  claudeSubagentScreen,
		"codex":   "› Viewing sub-agent — direct input is disabled\n",
		"copilot": "┃ Message this subagent (idle -- will wake it)...\n Esc back to tasks\n",
	} {
		p := newPane("idle")
		p.kind, p.screens = kind, []string{screen}
		var errb bytes.Buffer
		code := promptAs(p.deps(), "builder", "task", deliverPolicy{confirm: true}, &errb)
		if code != exitcode.HerdrError || !strings.HasPrefix(errb.String(), "agent_input_elsewhere\t") || len(p.prompts) != 0 {
			t.Errorf("%s: exit %d, stderr %q, prompts %v", kind, code, errb.String(), p.prompts)
		}
	}
}

// Codex's screens, cut down from internal/agent/testdata/screens.
const (
	codexHooksScreen = "  Hooks need review\n  6 hooks are new or changed.\n\n› 1. Review hooks\n  2. Trust all and continue\n\n  enter confirm · esc skip\n"
	codexMainScreen  = "› Ask Codex to do anything\n\n  ← for agents · ? for shortcuts\n"
)

// Codex 0.167.0 opened on its hook review prompt, Herdr said idle, and the
// pair was called ready (F29). The pane is read before a pair is called ready.
func TestAgentReadyRefusesAPaneAtADialogHerdrCallsIdle(t *testing.T) {
	p := newPane("idle")
	p.kind = "codex"
	p.screens = []string{codexHooksScreen}
	var errb bytes.Buffer
	code := agentReadyCmd(p.deps(), "builder", readyWithin, &errb)
	if code != exitcode.ManualGate || !strings.HasPrefix(errb.String(), "agent_not_at_input\t") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	for _, want := range []string{"Hooks need review", "2. Trust all and continue"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr should name the dialog and show the screen (%q): %q", want, errb.String())
		}
	}
	if len(p.prompts) != 0 {
		t.Errorf("typed %v", p.prompts)
	}
}

func TestAgentReadyPassesAPaneAtItsInput(t *testing.T) {
	p := newPane("idle")
	p.kind = "codex"
	p.screens = []string{codexMainScreen}
	var errb bytes.Buffer
	if code := agentReadyCmd(p.deps(), "builder", readyWithin, &errb); code != exitcode.OK || errb.Len() != 0 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

// A pane still drawing gets a moment: the dialog a CLI opens on can close by
// itself, as a splash screen does.
func TestAgentReadyWaitsForTheInputToAppear(t *testing.T) {
	p := newPane("idle")
	p.kind = "codex"
	p.screens = []string{codexHooksScreen, codexMainScreen}
	if code := agentReadyCmd(p.deps(), "builder", readyWithin, io.Discard); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
}

// A dialog Herdr reports itself is refused too, whatever the screen reads as.
func TestAgentReadyRefusesAPaneHerdrCallsBlocked(t *testing.T) {
	p := newPane("blocked")
	p.screens = []string{"Do you trust the files in this folder?\n"}
	var errb bytes.Buffer
	code := agentReadyCmd(p.deps(), "reviewer", readyWithin, &errb)
	if code != exitcode.ManualGate || !strings.Contains(errb.String(), "Do you trust the files") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

// A screen nothing recognises passes: a wrong refusal would stop every pairing.
func TestAgentReadyPassesAScreenItDoesNotKnow(t *testing.T) {
	p := newPane("idle")
	p.kind = "codex"
	p.screens = []string{"a screen from a later release\n"}
	if code := agentReadyCmd(p.deps(), "builder", readyWithin, io.Discard); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
}
