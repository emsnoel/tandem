package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func init() { Register(claudeAdapter{}) }

// claudeAdapter maps onto Claude Code, which fuses approval and sandboxing
// into a single --permission-mode enum that also carries a persona value
// (`plan`). tandem uses that enum for autonomy only.
type claudeAdapter struct{}

func (claudeAdapter) Kind() string { return "claude" }

func (claudeAdapter) Supports(axis string) []string {
	switch axis {
	case "autonomy":
		return []string{"readonly", "ask", "auto", "full"}
	case "effort":
		// xhigh exists natively but is deliberately unreachable through the
		// normalised vocabulary; user-config args can still select it.
		return []string{"low", "medium", "high", "max"}
	case "subagent_effort":
		// Claude Code agent definitions carry a default `model:`, and the
		// dispatch-time model parameter overrides it. That is what lets a
		// staffing advisory name a floor without pinning an agent to one
		// model.
		return []string{"low", "medium", "high", "max"}
	default:
		return nil
	}
}

// Persona returns none: --agent names a configured agent that may not exist on
// this machine, and inventing one would fail at launch.
func (claudeAdapter) Persona(Role) string { return "" }

func (c claudeAdapter) Resolve(s Spec) (Launch, error) {
	l := Launch{Kind: c.Kind(), Warnings: []Warning{}}

	autonomy, w, err := Degrade("autonomy", string(s.Autonomy), AutonomyOrder,
		c.Supports("autonomy"), s.AllowDegrade, s.AllowWiden, c.Kind())
	if err != nil {
		return Launch{}, err
	}
	if w != nil {
		l.Warnings = append(l.Warnings, *w)
	}
	switch Autonomy(autonomy) {
	case Readonly:
		l.Args = append(l.Args, "--permission-mode", "plan")
	case Ask:
		l.Args = append(l.Args, "--permission-mode", "manual")
	case Auto:
		// Auto mode lets the CLI decide each action without asking, like Codex's
		// --ask-for-approval never. acceptEdits still asks before shell commands,
		// which would stall an unattended pane.
		l.Args = append(l.Args, "--permission-mode", "auto")
	case Full:
		l.Args = append(l.Args, "--permission-mode", "bypassPermissions")
	}

	if s.Model != "" {
		l.Args = append(l.Args, "--model", s.Model)
	}

	effort, w, err := Degrade("effort", string(s.Effort), EffortOrder,
		c.Supports("effort"), s.AllowDegrade, s.AllowWiden, c.Kind())
	if err != nil {
		return Launch{}, err
	}
	if w != nil {
		l.Warnings = append(l.Warnings, *w)
	}
	if effort != "" {
		l.Args = append(l.Args, "--effort", effort)
	}

	lean, err := c.leanArgs(s)
	if err != nil {
		return Launch{}, err
	}
	l.Args = append(l.Args, lean...)

	// No cwd flag: the pane's working directory applies, and tandem-pair
	// already sets it when it splits the pane.
	l.Args = append(l.Args, s.ExtraArgs...)
	l.Sandboxed = c.Sandboxed(s)
	return l, nil
}

// claudeHerdrHook runs Herdr's Claude integration script. Herdr installs it
// under $CLAUDE_CONFIG_DIR, or ~/.claude when that is unset, and runs it with
// `session` on every SessionStart. Without the integration the command does
// nothing, so Claude shows no hook error.
const claudeHerdrHook = `f="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/hooks/herdr-agent-state.sh"; [ ! -f "$f" ] || bash "$f" session`

// ClaudeLeanSettings is the only settings a lean Claude agent gets beyond the
// project's own. It keeps Herdr's session hook, so Herdr still learns which
// session the pane holds after a launch and after /clear. Claude reads a JSON
// string passed to --settings, so tandem writes no file.
var ClaudeLeanSettings = func() string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type entry struct {
		Hooks []hook `json:"hooks"`
	}
	b, err := json.Marshal(map[string]map[string][]entry{
		"hooks": {"SessionStart": {{Hooks: []hook{{Type: "command", Command: claudeHerdrHook, Timeout: 10}}}}},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// leanArgs keeps the user's own Claude setup out of a builder or reviewer:
//   - --setting-sources project,local drops user settings, which hold the
//     enabled plugins, user hooks and output style.
//   - --settings adds back Herdr's session hook and nothing else.
//   - --strict-mcp-config drops every MCP server not given with --mcp-config.
//
// The user keeps any of these flags they pass in adapters.claude.args.
func (claudeAdapter) leanArgs(s Spec) ([]string, error) {
	switch s.Isolation {
	case "", IsolationLean:
	case IsolationNone:
		return nil, nil
	default:
		return nil, &Error{
			Code: "isolation_unknown", Axis: "isolation", Value: string(s.Isolation), Adapter: "claude",
			Supported: IsolationValues, Exit: 20,
			Remedy: fmt.Sprintf(`%q is not an isolation value; set "isolation" under adapters.claude in ~/.config/herdr/tandem.json to "lean" or "none"`, s.Isolation),
		}
	}
	if !s.Role.Unattended() {
		return nil, nil
	}
	var out []string
	for _, f := range [][]string{
		{"--setting-sources", "project,local"},
		{"--settings", ClaudeLeanSettings},
		{"--strict-mcp-config"},
	} {
		if !passesFlag(s.ExtraArgs, f[0]) {
			out = append(out, f...)
		}
	}
	return out, nil
}

// passesFlag reports whether args name flag, as `flag` or `flag=value`.
func passesFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// Sandboxed is always false: Claude Code does not deny the Herdr socket.
func (claudeAdapter) Sandboxed(Spec) bool { return false }

// ResetCommand: verified with Claude Code in a Herdr pane.
func (claudeAdapter) ResetCommand() string { return "/clear" }

// ResetConfirmKey: /clear opens no dialog.
func (claudeAdapter) ResetConfirmKey() string { return "" }

// InstructionFiles: Claude Code loads the project's CLAUDE.md into every
// conversation, including one started with /clear. It does not load AGENTS.md.
func (claudeAdapter) InstructionFiles() []string { return []string{"CLAUDE.md"} }
