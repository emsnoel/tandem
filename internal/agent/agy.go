package agent

import "strings"

func init() { Register(agyAdapter{}) }

// agyAdapter maps onto Antigravity's agy CLI, measured against agy 1.5.14.
//
// agy defaults to Auto, which runs commands sandboxed and without asking, so
// `ask` must set the agentMode setting to "" explicitly. Auto blocks the Herdr
// socket, so a builder in readonly or auto mode needs the mailbox. Isolation
// is ignored, as for Copilot: agy has no flag for it.
type agyAdapter struct{}

func (agyAdapter) Kind() string { return "agy" }

func (agyAdapter) Supports(axis string) []string {
	switch axis {
	case "autonomy":
		return []string{"readonly", "ask", "auto", "full"}
	case "effort":
		return []string{"low", "medium", "high", "max"}
	default:
		return nil
	}
}

// Persona returns none: agy has no persona flag.
func (agyAdapter) Persona(Role) string { return "" }

func (a agyAdapter) Resolve(s Spec) (Launch, error) {
	l := Launch{Kind: a.Kind(), Warnings: []Warning{}}

	autonomy, w, err := Degrade("autonomy", string(s.Autonomy), AutonomyOrder,
		a.Supports("autonomy"), s.AllowDegrade, s.AllowWiden, a.Kind())
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
		l.Args = append(l.Args, "--settings", `{"agentMode":""}`)
	case Auto:
		l.Args = append(l.Args, "--mode", "auto")
		// Auto's review of a command can stop and ask. In a live pair it held
		// a reviewer's tandem-verdict at "Run this command?", and nobody sits
		// at an unattended pane. An allow rule skips that review, so the
		// tandem commands the prompts name are pre-approved. A planner keeps
		// the question: it is somebody's own pane.
		if s.Role.Unattended() {
			l.Args = append(l.Args, "--settings", agyTandemAllow)
		}
	case Full:
		l.Args = append(l.Args, "--dangerously-skip-permissions")
	}

	if s.Model != "" {
		l.Args = append(l.Args, "--model", s.Model)
	}

	effort, ew, err := Degrade("effort", string(s.Effort), EffortOrder,
		a.Supports("effort"), s.AllowDegrade, s.AllowWiden, a.Kind())
	if err != nil {
		return Launch{}, err
	}
	if ew != nil {
		l.Warnings = append(l.Warnings, *ew)
	}
	if effort != "" {
		l.Args = append(l.Args, "--effort", effort)
		if suffix := agyModelSuffix(s.Model); suffix != "" && suffix != effort {
			l.Warnings = append(l.Warnings, Warning{
				Axis:      "effort",
				Requested: effort,
				Direction: Narrowing, DirectionS: "narrowing",
				Detail: "agy uses --effort " + effort + " over the suffix -" + suffix +
					" of model " + s.Model,
			})
		}
	}

	l.Args = append(l.Args, s.ExtraArgs...)
	l.Sandboxed = a.Sandboxed(s)
	return l, nil
}

// agyTandemAllow is an agy settings document that pre-approves the commands a
// builder or reviewer prompt tells the agent to run. agy 1.5.14 stores
// "always allow" choices in this shape, and command(X) matches every command
// that starts with X.
const agyTandemAllow = `{"permissions":{"allow":["command(tandem-ask)","command(tandem-handback)","command(tandem-verdict)"]}}`

func agyModelSuffix(model string) string {
	for _, suffix := range []string{"xhigh", "medium", "high", "low"} {
		if strings.HasSuffix(model, "-"+suffix) {
			return suffix
		}
	}
	return ""
}

// Sandboxed is true for readonly and auto, when autonomy is unset (agy then
// starts in its own default, Auto), and when the user adds --sandbox.
func (agyAdapter) Sandboxed(s Spec) bool {
	return s.Autonomy == "" || s.Autonomy == Readonly || s.Autonomy == Auto || containsArg(s.ExtraArgs, "--sandbox")
}

// ResetCommand: /new, an alias of /clear in agy 1.5.14. It starts a new
// conversation in the same process.
func (agyAdapter) ResetCommand() string { return "/new" }

// InstructionFiles: unmeasured, so the launch prompt keeps asking for the
// conventions file.
func (agyAdapter) InstructionFiles() []string { return nil }

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
