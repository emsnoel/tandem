package agent

import (
	"reflect"
	"testing"
)

func resolve(t *testing.T, s Spec) Launch {
	t.Helper()
	a, err := Get(s.Kind)
	if err != nil {
		t.Fatalf("Get(%q): %v", s.Kind, err)
	}
	l, err := a.Resolve(s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return l
}

func TestCodexAutonomyMapping(t *testing.T) {
	cases := map[Autonomy][]string{
		Readonly: {"--sandbox", "read-only", "--ask-for-approval", "on-request"},
		Ask:      {"--sandbox", "workspace-write", "--ask-for-approval", "on-request"},
		Auto:     {"--sandbox", "workspace-write", "--ask-for-approval", "never"},
		Full:     {"--dangerously-bypass-approvals-and-sandbox"},
	}
	for autonomy, want := range cases {
		l := resolve(t, Spec{Kind: "codex", Role: RolePlanner, Autonomy: autonomy})
		if !reflect.DeepEqual(l.Args, want) {
			t.Errorf("autonomy=%s: got %v, want %v", autonomy, l.Args, want)
		}
	}
}

func TestCodexModelEffortAndCwd(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "codex", Role: RoleBuilder, Autonomy: Auto,
		Model: "gpt-5.4", Effort: EffortHigh, Cwd: "/repo",
	})
	want := []string{
		"--sandbox", "workspace-write", "--ask-for-approval", "never",
		"-c", "check_for_update_on_startup=false",
		"--model", "gpt-5.4",
		"-c", "model_reasoning_effort=high",
		"--cd", "/repo",
	}
	if !reflect.DeepEqual(l.Args, want) {
		t.Fatalf("got %v, want %v", l.Args, want)
	}
}

// Codex's "Update available" prompt blocks the pane at startup. Nobody sits at
// a builder or a reviewer to answer it, so those roles start with the update
// check off (measured on codex-cli 0.167.0, F30). A planner keeps the prompt.
func TestCodexUnattendedRolesSkipTheUpdateCheck(t *testing.T) {
	off := []string{"-c", "check_for_update_on_startup=false"}
	has := func(args []string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == off[0] && args[i+1] == off[1] {
				return true
			}
		}
		return false
	}
	for _, role := range []Role{RoleBuilder, RoleReviewer} {
		for _, autonomy := range []Autonomy{Readonly, Ask, Auto, Full} {
			if l := resolve(t, Spec{Kind: "codex", Role: role, Autonomy: autonomy}); !has(l.Args) {
				t.Errorf("%s at %s starts with the update check on: %v", role, autonomy, l.Args)
			}
		}
	}
	if l := resolve(t, Spec{Kind: "codex", Role: RolePlanner, Autonomy: Auto}); has(l.Args) {
		t.Errorf("a planner has a person at the pane and keeps the prompt: %v", l.Args)
	}
}

// Codex selects a persona through user-defined profiles, which tandem
// cannot assume exist, so role must not emit one.
func TestCodexHasNoDerivedPersona(t *testing.T) {
	a, _ := Get("codex")
	if p := a.Persona(RoleBuilder); p != "" {
		t.Fatalf("got persona %q, want none", p)
	}
}

func TestCodexSandboxedUnlessFull(t *testing.T) {
	a, _ := Get("codex")
	if !a.Sandboxed(Spec{Autonomy: Auto}) {
		t.Error("autonomy=auto must be predicted sandboxed")
	}
	if a.Sandboxed(Spec{Autonomy: Full}) {
		t.Error("autonomy=full must not be predicted sandboxed")
	}
}

func TestCodexExtraArgsComeLast(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "codex", Role: RoleBuilder, Autonomy: Full,
		ExtraArgs: []string{"--search"},
	})
	if l.Args[len(l.Args)-1] != "--search" {
		t.Fatalf("extra args must come last; got %v", l.Args)
	}
}

func TestClaudeAutonomyMapping(t *testing.T) {
	cases := map[Autonomy][]string{
		Readonly: {"--permission-mode", "plan"},
		Ask:      {"--permission-mode", "manual"},
		// Claude's auto mode, not acceptEdits, which still asks before every shell
		// command and would stall an unattended pane.
		Auto: {"--permission-mode", "auto"},
		Full: {"--permission-mode", "bypassPermissions"},
	}
	for autonomy, want := range cases {
		l := resolve(t, Spec{Kind: "claude", Role: RoleBuilder, Autonomy: autonomy})
		if !reflect.DeepEqual(l.Args, want) {
			t.Errorf("autonomy=%s: got %v, want %v", autonomy, l.Args, want)
		}
	}
}

func TestClaudeModelAndEffort(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "claude", Role: RolePlanner, Autonomy: Ask,
		Model: "opus", Effort: EffortMax,
	})
	want := []string{"--permission-mode", "manual", "--model", "opus", "--effort", "max"}
	if !reflect.DeepEqual(l.Args, want) {
		t.Fatalf("got %v, want %v", l.Args, want)
	}
}

// Claude has no cwd flag; the pane's own working directory applies.
func TestClaudeIgnoresCwd(t *testing.T) {
	l := resolve(t, Spec{Kind: "claude", Role: RoleBuilder, Autonomy: Full, Cwd: "/repo"})
	for _, a := range l.Args {
		if a == "/repo" {
			t.Fatalf("cwd must not become a flag; got %v", l.Args)
		}
	}
}

// --agent names a configured agent, which may not exist on the machine, so
// role must not emit one.
func TestClaudeHasNoDerivedPersona(t *testing.T) {
	a, _ := Get("claude")
	if p := a.Persona(RolePlanner); p != "" {
		t.Fatalf("got persona %q, want none", p)
	}
}

func TestClaudeNeverSandboxed(t *testing.T) {
	a, _ := Get("claude")
	if a.Sandboxed(Spec{Autonomy: Ask}) {
		t.Error("claude does not sandbox the Herdr socket")
	}
}

// adapter is the Adapter-returning sibling of resolve. Get's error is checked
// rather than discarded, so a kind that is not registered yet fails the test
// with a readable message instead of panicking on a nil interface.
func adapter(t *testing.T, kind string) Adapter {
	t.Helper()
	a, err := Get(kind)
	if err != nil {
		t.Fatalf("Get(%q): %v", kind, err)
	}
	return a
}

func TestCopilotAutonomyMapping(t *testing.T) {
	// Role is builder throughout, so --no-ask-user rides along on every case;
	// TestCopilotDisablesAskUserForBuilderOnly is what pins that flag's rule.
	cases := map[Autonomy][]string{
		Readonly: {"--mode", "plan", "--no-ask-user"},
		Ask:      {"--no-ask-user"},
		Auto:     {"--allow-all-tools", "--allow-all-urls", "--allow-all-paths", "--no-ask-user"},
		Full:     {"--allow-all", "--no-ask-user"},
	}
	for autonomy, want := range cases {
		l := resolve(t, Spec{Kind: "copilot", Role: RoleBuilder, Autonomy: autonomy})
		if !reflect.DeepEqual(l.Args, want) {
			t.Errorf("autonomy=%s: got %v, want %v", autonomy, l.Args, want)
		}
	}
}

// Copilot has no flag for manual approval: it is the CLI's own default, set by
// defaultPermissionMode in ~/.copilot/settings.json. `ask` is therefore the
// absence of every autonomy flag, and this pins that it emits nothing at all
// rather than something approximate.
func TestCopilotAskEmitsNoFlags(t *testing.T) {
	l := resolve(t, Spec{Kind: "copilot", Role: RolePlanner, Autonomy: Ask})
	if len(l.Args) != 0 {
		t.Fatalf("got %v, want no args", l.Args)
	}
}

func TestCopilotModelEffortAndCwd(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "copilot", Role: RoleBuilder, Autonomy: Auto,
		Model: "claude-sonnet-5", Effort: EffortMax, Cwd: "/repo",
	})
	want := []string{
		"--allow-all-tools", "--allow-all-urls", "--allow-all-paths", "--no-ask-user",
		"--model", "claude-sonnet-5",
		"--effort", "max",
		"-C", "/repo",
	}
	if !reflect.DeepEqual(l.Args, want) {
		t.Fatalf("got %v, want %v", l.Args, want)
	}
}

// ask_user asks a HUMAN. A builder has none, so a call to it is a pane that
// reports idle and waits forever; tandem-ask is the channel that reaches
// somebody. A planner keeps the tool: there is a person in front of it.
func TestCopilotDisablesAskUserForBuilderOnly(t *testing.T) {
	l := resolve(t, Spec{Kind: "copilot", Role: RoleBuilder, Autonomy: Auto})
	if !contains(l.Args, "--no-ask-user") {
		t.Errorf("an unattended builder must not be able to stall on ask_user; got %v", l.Args)
	}
	l = resolve(t, Spec{Kind: "copilot", Role: RolePlanner, Autonomy: Ask})
	if contains(l.Args, "--no-ask-user") {
		t.Errorf("a planner has a human to answer; got %v", l.Args)
	}
}

// max is a real --effort value here, unlike Codex, where it is model-specific
// and the model-agnostic adapter withholds it.
func TestCopilotSupportsMaxEffort(t *testing.T) {
	a := adapter(t, "copilot")
	if !contains(a.Supports("effort"), "max") {
		t.Fatalf("got %v, want max included", a.Supports("effort"))
	}
}

// --agent names a custom agent that may not exist on the machine, and --mode
// is already spent on autonomy, so role must not emit a persona.
func TestCopilotHasNoDerivedPersona(t *testing.T) {
	a := adapter(t, "copilot")
	if p := a.Persona(RoleBuilder); p != "" {
		t.Fatalf("got persona %q, want none", p)
	}
}

// Copilot's MXC sandbox is experimental and off by default, so the Herdr
// socket is reachable at every autonomy value and the mailbox is never needed.
func TestCopilotNeverSandboxed(t *testing.T) {
	a := adapter(t, "copilot")
	for _, autonomy := range []Autonomy{Readonly, Ask, Auto, Full} {
		if a.Sandboxed(Spec{Autonomy: autonomy}) {
			t.Errorf("autonomy=%s must not be predicted sandboxed", autonomy)
		}
	}
}

func TestCopilotExtraArgsComeLast(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "copilot", Role: RoleBuilder, Autonomy: Full,
		ExtraArgs: []string{"--experimental"},
	})
	if l.Args[len(l.Args)-1] != "--experimental" {
		t.Fatalf("extra args must come last; got %v", l.Args)
	}
}

// A fresh session id is what keeps Copilot's session-restore picker off the
// screen. Herdr reports a pane sitting on that picker as IDLE, so an
// unattended builder would be handed a plan that goes nowhere.
func TestCopilotSessionIDForBuilderOnly(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "copilot", Role: RoleBuilder, Autonomy: Auto,
		SessionID: "11111111-2222-3333-4444-555555555555",
	})
	if argAfter(l.Args, "--session-id") != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("builder must start a new session; got %v", l.Args)
	}
	// A planner is somebody's own pane; forcing a new session would throw
	// away the conversation they are sitting in.
	l = resolve(t, Spec{
		Kind: "copilot", Role: RolePlanner, Autonomy: Ask,
		SessionID: "11111111-2222-3333-4444-555555555555",
	})
	if contains(l.Args, "--session-id") {
		t.Errorf("a planner must keep its own session; got %v", l.Args)
	}
}

// An unset SessionID emits nothing, so every other test's expected argv holds.
func TestCopilotNoSessionIDWhenUnset(t *testing.T) {
	l := resolve(t, Spec{Kind: "copilot", Role: RoleBuilder, Autonomy: Auto})
	if contains(l.Args, "--session-id") {
		t.Fatalf("got %v, want no --session-id", l.Args)
	}
}

// argAfter returns the element following flag, or "" when absent.
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestKindsRegistered(t *testing.T) {
	want := []string{"claude", "codex", "copilot"}
	if !reflect.DeepEqual(Kinds(), want) {
		t.Fatalf("got %v, want %v", Kinds(), want)
	}
}

func TestCopilotReviewerIsUnattended(t *testing.T) {
	l := resolve(t, Spec{Kind: "copilot", Role: RoleReviewer, SessionID: "abc", Autonomy: Auto})
	if !contains(l.Args, "--no-ask-user") {
		t.Errorf("a reviewer has no human to answer ask_user; got %v", l.Args)
	}
	if argAfter(l.Args, "--session-id") != "abc" {
		t.Errorf("a reviewer must get the session id; got %v", l.Args)
	}
	l = resolve(t, Spec{Kind: "copilot", Role: RolePlanner, SessionID: "abc", Autonomy: Auto})
	if contains(l.Args, "--no-ask-user") || contains(l.Args, "--session-id") {
		t.Errorf("a planner gets neither flag; got %v", l.Args)
	}
}

// A planner is somebody's own pane, so a path dialog there has a person to
// answer it. Only the unattended roles are given every path.
func TestCopilotAllowsAllPathsForUnattendedRolesOnly(t *testing.T) {
	has := func(args []string) bool {
		for _, a := range args {
			if a == "--allow-all-paths" {
				return true
			}
		}
		return false
	}
	for role, want := range map[Role]bool{RoleBuilder: true, RoleReviewer: true, RolePlanner: false} {
		l := resolve(t, Spec{Kind: "copilot", Role: role, Autonomy: Auto})
		if got := has(l.Args); got != want {
			t.Errorf("role=%s: --allow-all-paths=%v, want %v (%v)", role, got, want, l.Args)
		}
	}
}
