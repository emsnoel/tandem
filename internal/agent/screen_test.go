package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each screen in testdata/screens is a real pane read with `herdr agent read
// --source visible` (paths replaced). Herdr reported each one idle or working,
// never blocked, so without this check a prompt would be typed into each.
func TestOffMainInputReadsRealScreens(t *testing.T) {
	cases := []struct {
		file, kind string
		off        bool
		why        string
	}{
		{"claude-main.txt", "claude", false, ""},
		{"claude-main-background.txt", "claude", false, ""},
		// A planner pane that was misread: a subagent's screen, its input addressed to
		// that subagent, and Herdr reporting idle.
		{"claude-subagent-idle.txt", "claude", true, "Write chaptered apprentice story"},
		{"claude-subagent-unfocused.txt", "claude", true, "Write chaptered apprentice story"},
		{"claude-subagent.txt", "claude", true, "Write chaptered apprentice story"},
		{"claude-subagent-list-on-main.txt", "claude", true, "Write chaptered apprentice story"},
		// The main screen with its agent list holding the keys: Enter picks a
		// row there, so typed text is left unsent.
		{"claude-agent-list.txt", "claude", true, "agent list"},
		// Up in the input recalls an earlier prompt and titles the rule: text
		// typed now would be sent joined to it.
		{"claude-history.txt", "claude", true, "earlier prompt"},
		// /tasks lists background work with no input box under it at all.
		{"claude-tasks.txt", "claude", true, "list"},
		// A finished subagent opened from /tasks: the state that planner was in.
		{"claude-task-view.txt", "claude", true, "Write harbour pilot story"},
		{"codex-main.txt", "codex", false, ""},
		{"codex-subagent.txt", "codex", true, "sub-agent"},
		{"codex-command-center.txt", "codex", true, "command center"},
		{"opencode-main.txt", "opencode", false, ""},
		{"opencode-child.txt", "opencode", true, "child session"},
		{"copilot-main.txt", "copilot", false, ""},
		{"copilot-main-tabbar.txt", "copilot", false, ""},
		// The sidebar open, with the input still holding the keys: a prompt
		// typed here started a turn in the main conversation.
		{"copilot-sidebar.txt", "copilot", false, ""},
		// A subagent's screen: a prompt typed here woke the subagent, which
		// answered it, while the main conversation never saw it.
		{"copilot-subagent.txt", "copilot", true, "subagent"},
		{"copilot-task-details.txt", "copilot", true, "subagent"},
		// Lists whose single keys act: x kills a task or closes a session, n
		// starts one, enter opens one.
		{"copilot-tasks.txt", "copilot", true, "task list"},
		// The same list with its finished task hidden: no row, so no "enter details".
		{"copilot-tasks-empty.txt", "copilot", true, "task list"},
		{"copilot-sidebar-focused.txt", "copilot", true, "session list"},
		{"copilot-tab.txt", "copilot", true, "session list"},
		{"copilot-github-tab.txt", "copilot", true, "GitHub tab"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", "screens", c.file))
			if err != nil {
				t.Fatal(err)
			}
			why, off := OffMainInput(c.kind, string(b))
			if off != c.off {
				t.Fatalf("OffMainInput(%s) = %v (%q), want %v", c.kind, off, why, c.off)
			}
			if !strings.Contains(why, c.why) {
				t.Errorf("why = %q, want it to name %q", why, c.why)
			}
		})
	}
}

// A screen read for one kind says nothing about another, and an empty or
// unknown screen is never called off: a wrong refusal holds a delivery for a
// person to clear, where Herdr's own status already guards the usual cases.
func TestOffMainInputIsSpecificToItsKind(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "screens", "claude-subagent-idle.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"codex", "opencode", "copilot", ""} {
		if why, off := OffMainInput(kind, string(b)); off {
			t.Errorf("a Claude subagent screen read as %q is off (%q)", kind, why)
		}
	}
	for _, kind := range []string{"claude", "codex", "opencode", "copilot"} {
		if why, off := OffMainInput(kind, ""); off {
			t.Errorf("an empty %s screen is off (%q)", kind, why)
		}
	}
}

// Claude's main screen draws a plain rule above its input box; only a
// subagent's screen puts a title in it. A rule elsewhere on the screen, as a
// transcript can hold, is not the input box's.
func TestClaudeOnlyTheRuleAboveTheInputCounts(t *testing.T) {
	main := strings.Join([]string{
		"⏺ Done.",
		"──────────────── a table heading ─",
		"text",
		"────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────",
		"  ⏵⏵ auto mode on (shift+tab to cycle)",
	}, "\n")
	if why, off := OffMainInput("claude", main); off {
		t.Errorf("a titled rule in the transcript was read as a subagent screen (%q)", why)
	}
}
