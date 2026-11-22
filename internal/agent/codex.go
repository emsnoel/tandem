package agent

import (
	"fmt"
	"strings"
)

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
	switch a := Autonomy(autonomy); a {
	case Readonly, Ask:
		l.Args, l.Granted = codexSandbox(a, s)
		l.Args = append(l.Args, "--ask-for-approval", "on-request")
	case Auto:
		l.Args, l.Granted = codexSandbox(a, s)
		l.Args = append(l.Args, "--ask-for-approval", "never")
	case Full:
		l.Args = append(l.Args, "--dangerously-bypass-approvals-and-sandbox")
	}

	// Codex opens on an "Update available" prompt when a newer release exists,
	// and nobody sits at a builder or a reviewer to answer it. With the check
	// off, 0.167.0 shows no prompt (F30). A planner keeps it.
	if s.Role.Unattended() {
		l.Args = append(l.Args, "-c", "check_for_update_on_startup=false")
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

// codexProfile names the permission profile tandem builds for a sandboxed
// Codex. The launch's -c overrides define it whole, so ~/.codex/config.toml
// needs no entry.
const codexProfile = "tandem"

// codexSandbox returns the -c overrides that start Codex in tandem's permission
// profile, and the part of the user's grant they apply. Measured on codex-cli
// 0.169.0 in a real agent (`codex sandbox` never starts the proxy):
//
//   - The profile extends Codex's own :workspace, or :read-only, so the
//     checkout's rules, .git kept read-only included, stay Codex's.
//   - unix_sockets lets commands connect to the Herdr socket, and bind and
//     connect Unix sockets under the checkout, such as a test's fake server.
//     Neither works unless network.enabled is true.
//   - network.enabled alone opens the whole network. The network_proxy
//     feature routes it through Codex's proxy, which allows no domain here, so
//     a direct connection out gets EPERM. allow_local_binding keeps loopback
//     open for a test server, TCP or UDP.
//   - A user's grant adds filesystem entries. A network grant drops the proxy,
//     which opens the internet too: Codex has no narrower setting. Gradle 9.2.0
//     needs it: its Java listener is refused behind the proxy although loopback
//     is open, and `./gradlew help --offline` passes with ~/.gradle and network.
//
// A read-only profile cannot create a socket file, so it lists only Herdr's,
// and it ignores the grant, since its sandbox writes nothing.
func codexSandbox(a Autonomy, s Spec) ([]string, SandboxGrant) {
	p := "permissions." + codexProfile + "."
	base, g := ":workspace", s.Sandbox
	var sockets []string
	if s.HerdrSocket != "" {
		sockets = append(sockets, s.HerdrSocket)
	}
	if a == Readonly {
		base, g = ":read-only", SandboxGrant{}
	} else if s.Cwd != "" {
		sockets = append(sockets, s.Cwd)
	}
	args := []string{
		"-c", "default_permissions=" + tomlString(codexProfile),
		"-c", p + "extends=" + tomlString(base),
		"-c", p + "network.enabled=true",
		"-c", p + "network.allow_local_binding=true",
	}
	if len(sockets) > 0 {
		args = append(args, "-c", p+"network.unix_sockets="+tomlTable(sockets, "allow"))
	}
	if len(g.WritableDirs) > 0 {
		args = append(args, "-c", p+"filesystem="+tomlTable(g.WritableDirs, "write"))
	}
	if !g.Network {
		args = append(args, "-c", "features.network_proxy=true")
	}
	return args, g
}

// tomlTable is an inline TOML table that maps each key to value.
func tomlTable(keys []string, value string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = tomlString(k) + "=" + tomlString(value)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// tomlString is s as a TOML basic string. Go's %q is close but writes \x
// escapes, which TOML rejects.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Sandboxed reports whether Codex runs its commands in a seatbelt sandbox.
// Only danger-full-access, which autonomy=full selects, has none.
func (codexAdapter) Sandboxed(s Spec) bool { return s.Autonomy != Full }

// ResetCommand: not /clear, which also wipes the pane's scrollback. The session
// id moves only when the next turn starts, so the reset is confirmed after the
// build prompt is delivered. In a Git checkout, /new first asks where the new
// conversation should run; ResetDialogKey answers it.
func (codexAdapter) ResetCommand() string { return "/new" }

// InstructionFiles: every Codex conversation, including after /new, opens with
// "# AGENTS.md instructions for <root>" and the file's text.
func (codexAdapter) InstructionFiles() []string { return []string{"AGENTS.md"} }
