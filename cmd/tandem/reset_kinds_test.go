package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

// A Claude builder with a Codex reviewer: the reviewer must be reset as Codex
// (/new plus the picker's Enter), not with the builder's /clear.
func TestResetUsesTheReviewersRecordedKind(t *testing.T) {
	root := withReviewer(t, rootWith(t, `{"kind":"claude","autonomy":"auto"}`, ""), `{"kind":"codex","name":"reviewer"}`)
	f := &fakeHerdr{
		screen:    checkoutPicker(t, ""),
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
		screen:    checkoutPicker(t, ""),
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
		screen:    checkoutPicker(t, ""),
		states:    []*herdr.Agent{kindAgent("codex", "idle", "s1", 5), kindAgent("codex", "idle", "another-panes", 5)},
		afterKeys: []*herdr.Agent{kindAgent("codex", "working", "another-panes", 6), kindAgent("codex", "idle", "another-panes", 7)},
	}
	tok, session, _ := runReset(f.registryDeps(), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if tok != "sent" || session != "" {
		t.Fatalf("got %q, %q; want sent, after the picker, naming no conversation", tok, session)
	}
	if len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Errorf("pressed %v; a session-only change is no proof, so Enter answers the picker once", f.keys)
	}
}

// A status event after the command proves the reset; the counter is not read.
func TestResetCountsAStatusEventAsSent(t *testing.T) {
	f := &fakeHerdr{states: []*herdr.Agent{
		kindAgent("codex", "idle", "s1", 5), kindAgent("codex", "working", "s1", 5), kindAgent("codex", "idle", "s1", 5),
	}}
	tok, _, err := runReset(f.deps("/new"), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "sent" {
		t.Fatalf("tok %q err %v, want sent", tok, err)
	}
	if len(f.keys) != 0 {
		t.Errorf("pressed %v after the reset was seen", f.keys)
	}
}

// Issue 88: Codex 0.169 shows the picker after /new while Herdr reports a
// status change. That change is not the new conversation, so the picker is
// still answered, and the reset waits for the change that follows Enter.
func TestResetAnswersThePickerEvenAfterAStatusEvent(t *testing.T) {
	f := &fakeHerdr{
		screen:    checkoutPicker(t, ""),
		states:    []*herdr.Agent{kindAgent("codex", "idle", "s1", 5), kindAgent("codex", "working", "s1", 6), kindAgent("codex", "idle", "s1", 7)},
		afterKeys: []*herdr.Agent{kindAgent("codex", "working", "s1", 8), kindAgent("codex", "idle", "s1", 9)},
	}
	tok, _, err := runReset(f.registryDeps(), "builder", rootWith(t, codexBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "sent" {
		t.Fatalf("got %q, %v; want sent", tok, err)
	}
	if len(f.keys) != 1 || f.keys[0] != "enter" {
		t.Errorf("pressed %v, want [enter]", f.keys)
	}
	if f.keyGets == 0 {
		t.Errorf("returned before the pane answered Enter")
	}
}

// The picker with its second row highlighted would create a new checkout on
// Enter, so it is left for a person, as is any other dialog.
func TestResetLeavesThePickerOnItsSecondRow(t *testing.T) {
	f := &fakeHerdr{
		screen: checkoutPicker(t, "-moved"),
		states: []*herdr.Agent{kindAgent("codex", "idle", "s1", 5)},
	}
	if _, _, err := runReset(f.registryDeps(), "builder", rootWith(t, codexBuilder, ""), 5*time.Second, false); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 0 {
		t.Errorf("pressed %v into a picker on its second row", f.keys)
	}
}

// checkoutPicker is Codex's "Where should the new conversation run?" screen
// from internal/agent/testdata. variant "" has its first row highlighted.
func checkoutPicker(t *testing.T, variant string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "testdata", "screens", "codex-new-conversation-picker"+variant+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
