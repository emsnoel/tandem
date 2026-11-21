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
		{Auto, []string{"--mode", "auto"}, true},
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

func TestAgyResolveModelAndEffort(t *testing.T) {
	s := agySpec(Auto)
	s.Model, s.Effort = "gemini-4-neon-high", EffortMax
	l, err := agyAdapter{}.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--mode", "auto", "--model", "gemini-4-neon-high", "--effort", "max"}
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
