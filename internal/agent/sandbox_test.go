package agent

import (
	"reflect"
	"testing"
)

// A user's sandbox grant reaches a workspace-write Codex sandbox as --add-dir
// and network_access. On codex-cli 0.169.0 a Gradle check needs both.
func TestCodexSandboxGrant(t *testing.T) {
	g := SandboxGrant{WritableDirs: []string{"/home/u/.gradle", "/opt/cache"}, Network: true}
	extra := []string{"--add-dir", "/home/u/.gradle", "--add-dir", "/opt/cache",
		"-c", "sandbox_workspace_write.network_access=true"}
	for _, autonomy := range []Autonomy{Ask, Auto} {
		l := resolve(t, Spec{Kind: "codex", Role: RoleReviewer, Autonomy: autonomy, Sandbox: g})
		if !reflect.DeepEqual(l.Args[4:10], extra) {
			t.Errorf("autonomy=%s: got %v, want the grant %v after the sandbox flags", autonomy, l.Args, extra)
		}
		if !reflect.DeepEqual(l.Granted, g) {
			t.Errorf("autonomy=%s: granted %+v, want %+v", autonomy, l.Granted, g)
		}
	}
}

// A read-only sandbox writes nothing, and full autonomy has no sandbox, so
// neither carries the grant.
func TestCodexSandboxGrantNeedsWorkspaceWrite(t *testing.T) {
	g := SandboxGrant{WritableDirs: []string{"/home/u/.gradle"}, Network: true}
	for _, autonomy := range []Autonomy{Readonly, Full} {
		l := resolve(t, Spec{Kind: "codex", Role: RoleReviewer, Autonomy: autonomy, Sandbox: g})
		for _, a := range l.Args {
			if a == "--add-dir" || a == "sandbox_workspace_write.network_access=true" {
				t.Errorf("autonomy=%s: got %v, want no grant", autonomy, l.Args)
			}
		}
		if !reflect.DeepEqual(l.Granted, SandboxGrant{}) {
			t.Errorf("autonomy=%s: granted %+v, want none", autonomy, l.Granted)
		}
	}
}

func TestSandboxGrantCovers(t *testing.T) {
	need := SandboxGrant{WritableDirs: []string{"/g"}, Network: true}
	cases := []struct {
		g    SandboxGrant
		want bool
	}{
		{SandboxGrant{WritableDirs: []string{"/x", "/g"}, Network: true}, true},
		{SandboxGrant{WritableDirs: []string{"/g"}}, false},
		{SandboxGrant{Network: true}, false},
		{SandboxGrant{}, false},
	}
	for _, c := range cases {
		if got := c.g.Covers(need); got != c.want {
			t.Errorf("%+v covers %+v: got %v, want %v", c.g, need, got, c.want)
		}
	}
}
