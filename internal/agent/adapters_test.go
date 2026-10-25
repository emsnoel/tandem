package agent

import (
	"reflect"
	"strings"
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
		l := resolve(t, Spec{Kind: "codex", Role: RoleBuilder, Autonomy: autonomy})
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
		"--model", "gpt-5.4",
		"-c", "model_reasoning_effort=high",
		"--cd", "/repo",
	}
	if !reflect.DeepEqual(l.Args, want) {
		t.Fatalf("got %v, want %v", l.Args, want)
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

func TestOpenCodeAutonomyMapping(t *testing.T) {
	// ask omits --auto; full passes it. Nothing else is expressible.
	l := resolve(t, Spec{Kind: "opencode", Role: RoleBuilder, Autonomy: Ask})
	for _, a := range l.Args {
		if a == "--auto" {
			t.Fatalf("autonomy=ask must not pass --auto; got %v", l.Args)
		}
	}
	l = resolve(t, Spec{Kind: "opencode", Role: RoleBuilder, Autonomy: Full})
	if !contains(l.Args, "--auto") {
		t.Fatalf("autonomy=full must pass --auto; got %v", l.Args)
	}
}

// opencode is the one adapter with a real persona to derive from role.
func TestOpenCodePersonaFollowsRole(t *testing.T) {
	l := resolve(t, Spec{Kind: "opencode", Role: RoleBuilder, Autonomy: Full})
	if argAfter(l.Args, "--agent") != "build" {
		t.Errorf("builder must select the build agent; got %v", l.Args)
	}
	l = resolve(t, Spec{Kind: "opencode", Role: RolePlanner, Autonomy: Ask})
	if argAfter(l.Args, "--agent") != "plan" {
		t.Errorf("planner must select the plan agent; got %v", l.Args)
	}
}

func TestOpenCodeModel(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "opencode", Role: RoleBuilder, Autonomy: Full,
		Model: "anthropic/claude-sonnet-5",
	})
	if argAfter(l.Args, "--model") != "anthropic/claude-sonnet-5" {
		t.Fatalf("got %v", l.Args)
	}
}

// readonly is a WIDENING here: the nearest supported value is `ask`, which
// grants write-with-prompts to an agent that asked to be read-only.
func TestOpenCodeReadonlyRefusedAsWidening(t *testing.T) {
	a, _ := Get("opencode")
	_, err := a.Resolve(Spec{Kind: "opencode", Role: RoleBuilder, Autonomy: Readonly})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	e := err.(*Error)
	if e.Exit != 21 || e.Direction != Widening {
		t.Fatalf("got exit=%d dir=%v, want 21/widening", e.Exit, e.Direction)
	}
}

// auto narrows to ask, which stalls an unattended pane, so it needs
// allow_degrade rather than allow_widen.
func TestOpenCodeAutoNarrowsToAsk(t *testing.T) {
	a, _ := Get("opencode")
	if _, err := a.Resolve(Spec{Kind: "opencode", Role: RoleBuilder, Autonomy: Auto}); err == nil {
		t.Fatal("expected a refusal without allow_degrade")
	}
	l, err := a.Resolve(Spec{
		Kind: "opencode", Role: RoleBuilder, Autonomy: Auto,
		AllowDegrade: []string{"autonomy"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(l.Args, "--auto") {
		t.Fatalf("narrowed to ask, so --auto must not appear; got %v", l.Args)
	}
	if len(l.Warnings) == 0 || l.Warnings[0].Direction != Narrowing {
		t.Fatalf("want a narrowing warning; got %+v", l.Warnings)
	}
}

func TestOpenCodeEffortUnsupported(t *testing.T) {
	a, _ := Get("opencode")
	if _, err := a.Resolve(Spec{
		Kind: "opencode", Role: RoleBuilder, Autonomy: Full, Effort: EffortHigh,
	}); err == nil {
		t.Fatal("opencode has no interactive effort control; expected a refusal")
	}
	l, err := a.Resolve(Spec{
		Kind: "opencode", Role: RoleBuilder, Autonomy: Full, Effort: EffortHigh,
		AllowDegrade: []string{"effort"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(l.Args, "--variant") {
		t.Fatalf("--variant is not available on the interactive path; got %v", l.Args)
	}
}

// A user --agent in ExtraArgs would land after the derived one and win, since
// opencode parses with yargs, so it is refused.
func TestOpenCodePersonaConflictRefused(t *testing.T) {
	a, _ := Get("opencode")
	_, err := a.Resolve(Spec{
		Kind: "opencode", Role: RoleBuilder, Autonomy: Full,
		ExtraArgs: []string{"--agent", "reviewer"},
	})
	if err == nil {
		t.Fatal("expected a refusal when args carry their own --agent")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.Code != "persona_conflict" || e.Exit != 20 {
		t.Fatalf("got code=%q exit=%d, want persona_conflict/20", e.Code, e.Exit)
	}
	if !strings.Contains(e.Remedy, "build") {
		t.Errorf("the remedy must name the persona being displaced; got %q", e.Remedy)
	}
}

func TestOpenCodeUnrelatedExtraArgsStillPass(t *testing.T) {
	l := resolve(t, Spec{
		Kind: "opencode", Role: RoleBuilder, Autonomy: Full,
		ExtraArgs: []string{"--pure"},
	})
	if l.Args[len(l.Args)-1] != "--pure" {
		t.Fatalf("unrelated extra args must survive; got %v", l.Args)
	}
}

func TestOpenCodeNeverSandboxed(t *testing.T) {
	a, _ := Get("opencode")
	if a.Sandboxed(Spec{Autonomy: Ask}) {
		t.Error("opencode has no sandbox, so it always reaches the socket")
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
	want := []string{"claude", "codex", "copilot", "opencode"}
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

func TestOpencodeReviewerPersonaIsBuild(t *testing.T) {
	if p := (opencodeAdapter{}).Persona(RoleReviewer); p != "build" {
		t.Errorf("reviewer persona = %q, want build", p)
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
