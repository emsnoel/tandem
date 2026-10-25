package agent

func init() { Register(codexAdapter{}) }

// codexAdapter maps onto codex-cli. Codex keeps approval and sandboxing on
// separate flags, so one normalised autonomy value expands to two.
type codexAdapter struct{}

func (codexAdapter) Kind() string { return "codex" }

func (codexAdapter) Supports(axis string) []string {
	switch axis {
	case "autonomy":
		return []string{"readonly", "ask", "auto", "full"}
	case "effort":
		// These are the normalised values supported by every model advertised
		// by the installed CLI. `max` is model-specific, so the model-agnostic
		// adapter omits it rather than claiming support it cannot guarantee.
		return []string{"low", "medium", "high"}
	case "subagent_effort":
		// codex-cli's spawn_agent takes `model` and `reasoning_effort` and honours
		// both, as seen in children's own turn_context in rollout logs. It is the same
		// knob as the effort axis above, so `max` is omitted for the same reason
		// (internal/agent/subagent_test.go pins them together). Project-scoped
		// .codex/agents/*.toml is not read, so a named persona cannot be selected.
		return []string{"low", "medium", "high"}
	default:
		return nil
	}
}

// Persona returns none: Codex selects one via -p/--profile, which names a
// user-defined profile tandem cannot assume exists.
func (codexAdapter) Persona(Role) string { return "" }

func (c codexAdapter) Resolve(s Spec) (Launch, error) {
	l := Launch{Kind: c.Kind(), Warnings: []Warning{}}

	autonomy, w, err := Degrade("autonomy", string(s.Autonomy), AutonomyOrder,
		c.Supports("autonomy"), s.AllowDegrade, s.AllowWiden, c.Kind())
	if err != nil {
		return Launch{}, err
	}
	if w != nil {
		l.Warnings = append(l.Warnings, *w)
	}
	workspace := []string{"--sandbox", "workspace-write"}
	switch Autonomy(autonomy) {
	case Readonly:
		l.Args = append(l.Args, "--sandbox", "read-only", "--ask-for-approval", "on-request")
	case Ask:
		l.Args = append(append(l.Args, workspace...), "--ask-for-approval", "on-request")
	case Auto:
		l.Args = append(append(l.Args, workspace...), "--ask-for-approval", "never")
	case Full:
		l.Args = append(l.Args, "--dangerously-bypass-approvals-and-sandbox")
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
		l.Args = append(l.Args, "-c", "model_reasoning_effort="+effort)
	}

	if s.Cwd != "" {
		l.Args = append(l.Args, "--cd", s.Cwd)
	}
	l.Args = append(l.Args, s.ExtraArgs...)
	l.Sandboxed = c.Sandboxed(s)
	return l, nil
}

// Sandboxed reports whether Codex's seatbelt will deny the Herdr socket.
// Only danger-full-access, which autonomy=full selects, clears it.
func (codexAdapter) Sandboxed(s Spec) bool { return s.Autonomy != Full }

// ResetCommand: not /clear, which also wipes the pane's scrollback. The session
// id moves only when the next turn starts, so the reset is confirmed after the
// build prompt is delivered.
func (codexAdapter) ResetCommand() string { return "/new" }

// ResetConfirmKey: /new opens a "Where should the new conversation run?"
// picker with "Current checkout" highlighted, while Herdr reports idle. Enter
// picks it.
func (codexAdapter) ResetConfirmKey() string { return "enter" }

// InstructionFiles: every Codex conversation, including after /new, opens with
// "# AGENTS.md instructions for <root>" and the file's text.
func (codexAdapter) InstructionFiles() []string { return []string{"AGENTS.md"} }
