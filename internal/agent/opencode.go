package agent

import "fmt"

func init() { Register(opencodeAdapter{}) }

// opencodeAdapter maps onto opencode's interactive TUI. It has no filesystem
// sandbox, so `--auto` is a boolean and autonomy is only {ask, full}. Its
// effort control, `--variant`, exists only on `opencode run`, not the TUI path
// Herdr starts, so effort cannot be expressed.
type opencodeAdapter struct{}

func (opencodeAdapter) Kind() string { return "opencode" }

func (opencodeAdapter) Supports(axis string) []string {
	switch axis {
	case "autonomy":
		// No sandbox: omitting --auto prompts (ask), passing it does not (full).
		return []string{"ask", "full"}
	case "effort":
		// --variant is `opencode run` only; unavailable on the TUI path.
		return nil
	case "subagent_effort":
		// Unverified. Unverified is not "probably yes": an adapter that
		// overclaims here would have the advisory name a floor nothing honours.
		return nil
	default:
		return nil
	}
}

// Persona maps role onto opencode's primary agents, which are named `build`
// and `plan` out of the box. This is the only adapter where the derived
// persona is real.
func (opencodeAdapter) Persona(r Role) string {
	if r == RolePlanner {
		return "plan"
	}
	return "build"
}

// Sandboxed is always false: opencode never denies the Herdr socket.
func (opencodeAdapter) Sandboxed(Spec) bool { return false }

func (o opencodeAdapter) Resolve(s Spec) (Launch, error) {
	l := Launch{Kind: o.Kind(), Warnings: []Warning{}}

	autonomy, w, err := Degrade("autonomy", string(s.Autonomy), AutonomyOrder,
		o.Supports("autonomy"), s.AllowDegrade, s.AllowWiden, o.Kind())
	if err != nil {
		return Launch{}, err
	}
	if w != nil {
		l.Warnings = append(l.Warnings, *w)
	}

	// Effort is resolved even though nothing can be emitted, so that an
	// unsupported request still fails closed rather than being ignored.
	_, ew, err := Degrade("effort", string(s.Effort), EffortOrder,
		o.Supports("effort"), s.AllowDegrade, s.AllowWiden, o.Kind())
	if err != nil {
		return Launch{}, err
	}
	if ew != nil {
		l.Warnings = append(l.Warnings, *ew)
	}

	// A user --agent in ExtraArgs would land after the derived one and win under
	// yargs, silently replacing the role, so it is refused.
	if indexOf(s.ExtraArgs, "--agent") >= 0 {
		return Launch{}, &Error{
			Code: "persona_conflict", Axis: "persona", Value: "--agent",
			Adapter: o.Kind(), Exit: 20,
			Remedy: fmt.Sprintf(
				"opencode's --agent in args conflicts with the role-derived persona %q; remove --agent from adapters.opencode.args, or the role would be silently overridden",
				o.Persona(s.Role)),
		}
	}

	// Autonomy is the presence of --auto: `full` passes it, `ask` passes nothing.
	if Autonomy(autonomy) == Full {
		l.Args = append(l.Args, "--auto")
	}

	if s.Model != "" {
		l.Args = append(l.Args, "--model", s.Model)
	}

	// Unconditional, unlike model and effort: a role always has a persona.
	l.Args = append(l.Args, "--agent", o.Persona(s.Role))

	l.Args = append(l.Args, s.ExtraArgs...)
	l.Sandboxed = o.Sandboxed(s)
	return l, nil
}

// ResetCommand: none that can be confirmed. /new does empty the conversation,
// but Herdr reports no session and no counter change, and opencode writes
// nothing until the next prompt. A durable run sends an opencode builder a task
// only with --share-context.
func (opencodeAdapter) ResetCommand() string { return "" }

// ResetConfirmKey: no reset command, so nothing to confirm.
func (opencodeAdapter) ResetConfirmKey() string { return "" }

// InstructionFiles: unmeasured, so the launch prompt keeps asking for the
// conventions file.
func (opencodeAdapter) InstructionFiles() []string { return nil }
