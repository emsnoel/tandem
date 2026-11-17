package agent

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// claudeSettings returns the value tandem passes to --settings, or fails.
func claudeSettings(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "--settings" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("no --settings in %v", args)
	return ""
}

// A builder or reviewer that tandem starts loads none of the user's own
// Claude setup: no user plugins, hooks, output style or MCP servers (#63).
func TestClaudeUnattendedLaunchIsLeanByDefault(t *testing.T) {
	for _, role := range []Role{RoleBuilder, RoleReviewer} {
		l := resolve(t, Spec{Kind: "claude", Role: role, Autonomy: Auto})
		want := []string{
			"--permission-mode", "auto",
			"--setting-sources", "project,local",
			"--settings", ClaudeLeanSettings,
			"--strict-mcp-config",
		}
		if !reflect.DeepEqual(l.Args, want) {
			t.Errorf("%s: got %v, want %v", role, l.Args, want)
		}
	}
}

// The settings carry Herdr's session hook and nothing else, so Herdr still
// learns the pane's session after a launch and after /clear.
func TestClaudeLeanSettingsHoldOnlyHerdrsSessionHook(t *testing.T) {
	var got struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	dec := json.NewDecoder(strings.NewReader(ClaudeLeanSettings))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("settings are not the expected JSON: %v\n%s", err, ClaudeLeanSettings)
	}
	if len(got.Hooks) != 1 || len(got.Hooks["SessionStart"]) != 1 {
		t.Fatalf("want one SessionStart entry and no other event; got %+v", got.Hooks)
	}
	entry := got.Hooks["SessionStart"][0]
	if entry.Matcher != "" {
		t.Errorf("a matcher would skip some session starts; got %q", entry.Matcher)
	}
	if len(entry.Hooks) != 1 || entry.Hooks[0].Type != "command" ||
		!strings.Contains(entry.Hooks[0].Command, "hooks/herdr-agent-state.sh") || entry.Hooks[0].Timeout <= 0 {
		t.Fatalf("want one command hook that runs Herdr's script; got %+v", entry.Hooks)
	}
}

// claudeHookCommand returns the shell command the lean settings run.
func claudeHookCommand(t *testing.T) string {
	t.Helper()
	var s struct {
		Hooks struct {
			SessionStart []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"SessionStart"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(ClaudeLeanSettings), &s); err != nil {
		t.Fatal(err)
	}
	return s.Hooks.SessionStart[0].Hooks[0].Command
}

// runHook runs the hook command the way Claude Code does, with sh -c and the
// hook's JSON on stdin.
func runHook(t *testing.T, env []string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", claudeHookCommand(t))
	cmd.Env = env
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fakeHerdrHook installs a stand-in for Herdr's script under dir/hooks. It
// records its arguments and stdin in dir/called.
func fakeHerdrHook(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "printf '%s\\n' \"$*\" > \"" + filepath.Join(dir, "called") + "\"\ncat >> \"" + filepath.Join(dir, "called") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "hooks", "herdr-agent-state.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCalled(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "called"))
	if err != nil {
		t.Fatalf("Herdr's hook did not run: %v", err)
	}
	return string(b)
}

// Herdr installs its Claude hook under $CLAUDE_CONFIG_DIR, or ~/.claude when
// that is unset. The command finds it in both places and passes `session` and
// the hook input through, as Herdr's own entry does.
func TestClaudeLeanHookRunsHerdrsScript(t *testing.T) {
	home := t.TempDir()
	fakeHerdrHook(t, filepath.Join(home, ".claude"))
	if out, err := runHook(t, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}); err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}
	if got := readCalled(t, filepath.Join(home, ".claude")); got != "session\n{\"hook_event_name\":\"SessionStart\"}" {
		t.Errorf("got %q", got)
	}

	cfg := t.TempDir()
	fakeHerdrHook(t, cfg)
	if out, err := runHook(t, []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + cfg, "PATH=" + os.Getenv("PATH")}); err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}
	if got := readCalled(t, cfg); !strings.HasPrefix(got, "session\n") {
		t.Errorf("CLAUDE_CONFIG_DIR was not honoured; got %q", got)
	}
}

// Without Herdr's integration the hook does nothing and succeeds, so Claude
// shows no hook error. tandem doctor reports the missing integration.
func TestClaudeLeanHookIsQuietWithoutHerdr(t *testing.T) {
	out, err := runHook(t, []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")})
	if err != nil || out != "" {
		t.Fatalf("got err=%v output=%q, want a silent success", err, out)
	}
}

// "isolation": "none" gives the agent the user's whole setup again.
func TestClaudeIsolationNoneLoadsTheUsersSetup(t *testing.T) {
	l := resolve(t, Spec{Kind: "claude", Role: RoleBuilder, Autonomy: Auto, Isolation: IsolationNone})
	if want := []string{"--permission-mode", "auto"}; !reflect.DeepEqual(l.Args, want) {
		t.Fatalf("got %v, want %v", l.Args, want)
	}
}

// The planner is the person's own pane. tandem never narrows its setup.
func TestClaudePlannerIsNeverIsolated(t *testing.T) {
	for _, iso := range []Isolation{"", IsolationLean} {
		l := resolve(t, Spec{Kind: "claude", Role: RolePlanner, Autonomy: Ask, Isolation: iso})
		if want := []string{"--permission-mode", "manual"}; !reflect.DeepEqual(l.Args, want) {
			t.Errorf("isolation=%q: got %v, want %v", iso, l.Args, want)
		}
	}
}

// A flag the user already passes in adapters.claude.args wins: tandem does not
// add its own copy, so the user's settings file is the one Claude reads.
func TestClaudeUserArgsReplaceTheMatchingLeanFlag(t *testing.T) {
	l := resolve(t, Spec{Kind: "claude", Role: RoleBuilder, Autonomy: Auto,
		ExtraArgs: []string{"--settings", "/mine.json", "--setting-sources=project"}})
	want := []string{
		"--permission-mode", "auto",
		"--strict-mcp-config",
		"--settings", "/mine.json", "--setting-sources=project",
	}
	if !reflect.DeepEqual(l.Args, want) {
		t.Fatalf("got %v, want %v", l.Args, want)
	}
}

func TestClaudeRefusesAnUnknownIsolation(t *testing.T) {
	a := adapter(t, "claude")
	_, err := a.Resolve(Spec{Kind: "claude", Role: RoleBuilder, Autonomy: Auto, Isolation: "nope"})
	var e *Error
	if !errors.As(err, &e) || e.Axis != "isolation" || e.Exit != 20 ||
		!strings.Contains(e.Remedy, `"lean"`) || !strings.Contains(e.Remedy, `"none"`) {
		t.Fatalf("got %v (%+v)", err, e)
	}
}
