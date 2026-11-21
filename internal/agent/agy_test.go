package agent

import (
	"reflect"
	"testing"
)

func agySpec(a Autonomy) Spec { return Spec{Role: RoleBuilder, Autonomy: a} }

func TestAgyResolveAutonomy(t *testing.T) {
	for _, c := range []struct {
		a         Autonomy
		args      []string
		sandboxed bool
	}{
		{Readonly, []string{"--mode", "plan"}, true},
		{Ask, []string{"--settings", `{"agentMode":""}`}, false},
		{Auto, []string{"--mode", "auto", "--settings", `{"permissions":{"allow":["command(tandem-ask)","command(tandem-handback)","command(tandem-verdict)"]}}`}, true},
		{Full, []string{"--dangerously-skip-permissions"}, false},
	} {
		l, err := agyAdapter{}.Resolve(agySpec(c.a))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(l.Args, c.args) || l.Sandboxed != c.sandboxed {
			t.Errorf("%s: args %q sandboxed %v, want %q %v", c.a, l.Args, l.Sandboxed, c.args, c.sandboxed)
		}
	}
}

// Auto's own review of a command can stop and ask, and nobody answers an
// unattended pane. It flagged tandem-verdict in a live pair, so an unattended
// agy at auto pre-approves the tandem commands its prompts tell it to run.
// A planner keeps agy's questions: a person sits at that pane.
func TestAgyAutoPreApprovesTandemCommandsOnlyWhenUnattended(t *testing.T) {
	for _, c := range []struct {
		role Role
		want []string
	}{
		{RoleBuilder, []string{"--mode", "auto", "--settings", agyTandemAllow}},
		{RoleReviewer, []string{"--mode", "auto", "--settings", agyTandemAllow}},
		{RolePlanner, []string{"--mode", "auto"}},
	} {
		l, err := agyAdapter{}.Resolve(Spec{Role: c.role, Autonomy: Auto})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(l.Args, c.want) {
			t.Errorf("%s: args %q, want %q", c.role, l.Args, c.want)
		}
	}
	for _, a := range []Autonomy{Readonly, Ask, Full} {
		l, _ := agyAdapter{}.Resolve(Spec{Role: RoleBuilder, Autonomy: a})
		if containsArg(l.Args, agyTandemAllow) {
			t.Errorf("%s: pre-approves tandem commands, but only auto runs Auto's review", a)
		}
	}
}

func TestAgyResolveModelAndEffort(t *testing.T) {
	s := agySpec(Auto)
	s.Model, s.Effort = "gemini-4-neon-high", EffortMax
	l, err := agyAdapter{}.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--mode", "auto", "--settings", agyTandemAllow, "--model", "gemini-4-neon-high", "--effort", "max"}
	if !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args %q, want %q", l.Args, want)
	}
}

func TestAgyWarnsWhenModelSuffixDisagrees(t *testing.T) {
	s := agySpec(Auto)
	s.Model, s.Effort = "gemini-4-neon-high", EffortLow
	l, _ := agyAdapter{}.Resolve(s)
	if len(l.Warnings) != 1 || l.Warnings[0].Axis != "effort" {
		t.Errorf("warnings %+v, want one on effort", l.Warnings)
	}
	s.Effort = EffortHigh
	l, _ = agyAdapter{}.Resolve(s)
	if len(l.Warnings) != 0 {
		t.Errorf("warnings %+v, want none", l.Warnings)
	}
}

func TestAgySupports(t *testing.T) {
	a := agyAdapter{}
	if got := a.Supports("autonomy"); !reflect.DeepEqual(got, []string{"readonly", "ask", "auto", "full"}) {
		t.Errorf("autonomy %v", got)
	}
	if got := a.Supports("effort"); !reflect.DeepEqual(got, []string{"low", "medium", "high", "max"}) {
		t.Errorf("effort %v", got)
	}
	if a.Supports("subagent_effort") != nil {
		t.Error("subagent_effort should be nil")
	}
}

func TestAgyIgnoresSessionIDAndCwd(t *testing.T) {
	s := agySpec(Ask)
	s.SessionID, s.Cwd = "abc", "/tmp/x"
	l, _ := agyAdapter{}.Resolve(s)
	if want := []string{"--settings", `{"agentMode":""}`}; !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args %q, want %q", l.Args, want)
	}
}

func TestAgyExtraArgsLastAndSandboxFlag(t *testing.T) {
	s := agySpec(Ask)
	s.ExtraArgs = []string{"--sandbox"}
	l, _ := agyAdapter{}.Resolve(s)
	if l.Args[len(l.Args)-1] != "--sandbox" || !l.Sandboxed {
		t.Errorf("args %q sandboxed %v", l.Args, l.Sandboxed)
	}
}
