package agent

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

	// No cwd flag: the pane's working directory applies, and tandem-pair
	// already sets it when it splits the pane.
	l.Args = append(l.Args, s.ExtraArgs...)
	l.Sandboxed = c.Sandboxed(s)
	return l, nil
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
