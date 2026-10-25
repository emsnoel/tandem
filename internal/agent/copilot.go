package agent

func init() { Register(copilotAdapter{}) }

// copilotAdapter maps onto GitHub Copilot CLI.
//
// Copilot splits permission across tools, filesystem paths and URLs. Its `ask`
// cannot be commanded: there is no --permission-mode, and manual approval
// comes from `defaultPermissionMode` in ~/.copilot/settings.json. So `ask` is
// the absence of every autonomy flag, and a machine set to allow-all runs more
// permissively than asked. The README says so.
type copilotAdapter struct{}

func (copilotAdapter) Kind() string { return "copilot" }

func (copilotAdapter) Supports(axis string) []string {
	switch axis {
	case "autonomy":
		return []string{"readonly", "ask", "auto", "full"}
	case "effort":
		// --effort also accepts none, minimal and xhigh, which the normalised
		// vocabulary has no name for; user-config args can still select them.
		// `max` is real here, unlike Codex, where it is model-specific.
		return []string{"low", "medium", "high", "max"}
	case "subagent_effort":
		// Unverified. Unverified is not "probably yes": an adapter that
		// overclaims here would have the advisory name a floor nothing honours.
		return nil
	default:
		return nil
	}
}

// Persona returns none: --agent names a custom agent that may not exist on this
// machine. Copilot's built-in --mode does carry `plan`, but this adapter spends
// that value on autonomy, exactly as the Claude Code one does.
func (copilotAdapter) Persona(Role) string { return "" }

func (c copilotAdapter) Resolve(s Spec) (Launch, error) {
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
		l.Args = append(l.Args, "--mode", "plan")
	case Ask:
		// Nothing to emit. Written as an explicit branch so the absence reads
		// as deliberate rather than forgotten.
	case Auto:
		// An unattended pane needs --allow-all-urls and --allow-all-paths, or a web
		// fetch or a path outside the repo opens a dialog nobody answers. Shell
		// commands are unconfined on Copilot anyway. A planner keeps the prompts.
		l.Args = append(l.Args, "--allow-all-tools", "--allow-all-urls")
		if s.Role.Unattended() {
			l.Args = append(l.Args, "--allow-all-paths")
		}
	case Full:
		l.Args = append(l.Args, "--allow-all")
	}

	// The ask_user tool asks a HUMAN, and a builder has none, so a call to it
	// is a pane reporting idle until someone notices. tandem-ask is the channel
	// that reaches somebody. A planner keeps the tool: it is somebody's own
	// pane.
	if s.Role.Unattended() {
		l.Args = append(l.Args, "--no-ask-user")
	}

	// A fresh session id suppresses Copilot's session-restore picker, which
	// appears whenever $COPILOT_HOME/open-sessions-state.json is non-empty, as it
	// is after every `herdr pane close`. Herdr reports a pane at that picker as
	// idle.
	if s.Role.Unattended() && s.SessionID != "" {
		l.Args = append(l.Args, "--session-id", s.SessionID)
	}

	if s.Model != "" {
		l.Args = append(l.Args, "--model", s.Model)
	}

	effort, ew, err := Degrade("effort", string(s.Effort), EffortOrder,
		c.Supports("effort"), s.AllowDegrade, s.AllowWiden, c.Kind())
	if err != nil {
		return Launch{}, err
	}
	if ew != nil {
		l.Warnings = append(l.Warnings, *ew)
	}
	if effort != "" {
		l.Args = append(l.Args, "--effort", effort)
	}

	if s.Cwd != "" {
		l.Args = append(l.Args, "-C", s.Cwd)
	}
	l.Args = append(l.Args, s.ExtraArgs...)
	l.Sandboxed = c.Sandboxed(s)
	return l, nil
}

// Sandboxed is always false: Copilot's sandbox is experimental and off by
// default, so shell commands and the Herdr socket are reachable, and a copilot
// builder needs no mailbox.
func (copilotAdapter) Sandboxed(Spec) bool { return false }

// ResetCommand: /new. Herdr cannot confirm it, because it keeps reporting the
// launch --session-id and its counter does not move. Copilot's process log
// names the new session, which confirms the reset
// (cmd/tandem/reset_copilot.go).
func (copilotAdapter) ResetCommand() string { return "/new" }

// ResetConfirmKey: /new opens no dialog.
func (copilotAdapter) ResetConfirmKey() string { return "" }

// InstructionFiles: unmeasured, so the launch prompt keeps asking for the
// conventions file.
func (copilotAdapter) InstructionFiles() []string { return nil }
