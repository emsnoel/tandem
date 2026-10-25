package agent

import (
	"strings"
	"testing"
)

// Each reset command was verified in a live pane: the new conversation had no
// memory of the previous turn. A kind without a verified command returns "".
func TestResetCommandsAreMeasured(t *testing.T) {
	for kind, want := range map[string]string{
		// Not /clear, which also wipes the terminal.
		"codex":  "/new",
		"claude": "/clear",
		// Confirmed from Copilot's process log, since Herdr's id never moves.
		"copilot": "/new",
		// /new works, but nothing observable confirms it.
		"opencode": "",
	} {
		a, err := Get(kind)
		if err != nil {
			t.Fatalf("Get(%q): %v", kind, err)
		}
		if got := a.ResetCommand(); got != want {
			t.Errorf("%s ResetCommand() = %q, want %q", kind, got, want)
		}
	}
}

// The key pressed once when a reset command changed nothing Herdr can see.
func TestResetConfirmKeysAreMeasured(t *testing.T) {
	for kind, want := range map[string]string{
		// /new opens a picker with "Current checkout" highlighted.
		"codex":    "enter",
		"claude":   "",
		"copilot":  "",
		"opencode": "",
	} {
		a, err := Get(kind)
		if err != nil {
			t.Fatalf("Get(%q): %v", kind, err)
		}
		if got := a.ResetConfirmKey(); got != want {
			t.Errorf("%s ResetConfirmKey() = %q, want %q", kind, got, want)
		}
	}
}

func TestResetDecision(t *testing.T) {
	for _, c := range []struct {
		name, cmd, status string
		empty             bool
		session, last     string
		want              string
	}{
		{"fresh after review", "/new", "idle", false, "s1", "", "go"},
		// "done" is a finished turn nobody has looked at yet.
		{"finished, unseen", "/new", "done", false, "s1", "", "go"},
		{"no verified command", "", "idle", false, "s1", "", "skip:unsupported"},
		{"mid-turn", "/new", "working", false, "s1", "", "skip:not-idle"},
		{"at a dialog", "/new", "blocked", false, "s1", "", "skip:not-idle"},
		{"known to have no conversation", "/new", "idle", true, "", "", "skip:no-session"},
		// A Codex conversation may be credited to another pane, so unknown
		// is reset rather than taken for empty.
		{"no session, conversation unknown", "/new", "idle", false, "", "", "go"},
		{"holds a build", "/new", "idle", false, "s1", "s1", "skip:holds-build"},
		{"an older build, since reset", "/new", "idle", false, "s2", "s1", "go"},
		{"no session is no build's", "/new", "idle", false, "", "", "go"},
		{"unsupported beats not-idle", "", "working", false, "", "", "skip:unsupported"},
		// Herdr never reports an opencode session, so no-session must not win.
		{"unsupported beats no-session", "", "idle", true, "", "", "skip:unsupported"},
	} {
		if got := ResetDecision(c.cmd, c.status, c.empty, c.session, c.last); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSettled(t *testing.T) {
	for status, want := range map[string]bool{
		"idle":    true,
		"done":    true,
		"working": false,
		"blocked": false,
		"unknown": false,
		"":        false,
	} {
		if got := Settled(status); got != want {
			t.Errorf("Settled(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestResetSentence(t *testing.T) {
	for _, c := range []struct {
		tok, purpose, want string
	}{
		{"reset", "build", "fresh conversation for the build (reset)."},
		{"sent", "review", "fresh conversation for the review (sent)."},
		{"would-reset", "review", "fresh conversation for the review (would-reset)."},
		{"skip:holds-build", "build", "the builder's conversation already holds a build; keeping it."},
		{"skip:unsupported", "review", "WARNING no measured way to reset this builder kind; the review shares its current conversation."},
		{"skip:not-idle", "build", "WARNING the builder is still working or at a dialog, so its conversation was not reset."},
		// Every first review hits this, so it does not warn.
		{"skip:no-session", "review", "the builder has no conversation yet (Herdr reports no session), so there is nothing to reset."},
		{"skip:no-identity", "build", "WARNING no builder identity is recorded (run tandem-pair), so its conversation was not reset."},
		{"skip:off-main", "build", "WARNING the builder's pane shows a subagent or a picker, not its conversation, so nothing was typed to reset it."},
		{"unconfirmed", "build", "WARNING could not reset the builder's conversation (unconfirmed); sending the build anyway."},
		{"", "review", "WARNING could not reset the builder's conversation (no answer); sending the review anyway."},
	} {
		if got := ResetSentence(c.tok, c.purpose, "builder"); got != c.want {
			t.Errorf("ResetSentence(%q, %q, builder)\n got %q\nwant %q", c.tok, c.purpose, got, c.want)
		}
	}
}

func TestResetSentenceNamesTheReviewer(t *testing.T) {
	for _, c := range []struct{ tok, want string }{
		{"sent", "fresh conversation for the review (sent)."},
		{"skip:holds-build", "the reviewer's conversation already holds a build; keeping it."},
		{"skip:unsupported", "WARNING no measured way to reset this reviewer kind; the review shares its current conversation."},
		{"skip:not-idle", "WARNING the reviewer is still working or at a dialog, so its conversation was not reset."},
		{"skip:no-session", "the reviewer has no conversation yet (Herdr reports no session), so there is nothing to reset."},
		{"skip:no-identity", "WARNING no reviewer identity is recorded (run tandem-pair), so its conversation was not reset."},
		{"unconfirmed", "WARNING could not reset the reviewer's conversation (unconfirmed); sending the review anyway."},
	} {
		got := ResetSentence(c.tok, "review", "reviewer")
		if got != c.want {
			t.Errorf("ResetSentence(%q, review, reviewer)\n got %q\nwant %q", c.tok, got, c.want)
		}
		if strings.Contains(got, "builder") {
			t.Errorf("ResetSentence(%q, review, reviewer) names the builder: %q", c.tok, got)
		}
	}
}

// Conventions files each CLI loads by itself, so the launch prompt need not
// ask for them. An empty list means the prompt keeps asking.
func TestInstructionFilesAreMeasured(t *testing.T) {
	for kind, want := range map[string]string{
		"codex":    "AGENTS.md",
		"claude":   "CLAUDE.md",
		"copilot":  "",
		"opencode": "",
	} {
		a, err := Get(kind)
		if err != nil {
			t.Fatalf("Get(%q): %v", kind, err)
		}
		if got := strings.Join(a.InstructionFiles(), ","); got != want {
			t.Errorf("%s InstructionFiles() = %q, want %q", kind, got, want)
		}
	}
}
