package agent

import (
	"reflect"
	"slices"
	"testing"
)

// profileArgs is the permission profile a sandboxed Codex starts with, in the
// order codexSandbox emits it.
func profileArgs(base string, rest ...string) []string {
	return append([]string{
		"-c", `default_permissions="tandem"`,
		"-c", `permissions.tandem.extends="` + base + `"`,
		"-c", "permissions.tandem.network.enabled=true",
		"-c", "permissions.tandem.network.allow_local_binding=true",
	}, rest...)
}

const proxyOn = "features.network_proxy=true"

// A workspace Codex may reach the Herdr socket and open Unix sockets inside its
// checkout. The network proxy, with no domain allowed, keeps the internet shut
// while loopback stays open. Measured on codex-cli 0.169.0 in a real agent.
func TestCodexSandboxProfile(t *testing.T) {
	cases := map[Autonomy][]string{
		Ask: profileArgs(":workspace",
			"-c", `permissions.tandem.network.unix_sockets={"/h/herdr.sock"="allow","/repo"="allow"}`,
			"-c", proxyOn, "--ask-for-approval", "on-request"),
		Auto: profileArgs(":workspace",
			"-c", `permissions.tandem.network.unix_sockets={"/h/herdr.sock"="allow","/repo"="allow"}`,
			"-c", proxyOn, "--ask-for-approval", "never"),
		// A read-only sandbox cannot create a socket file, so only Herdr's is listed.
		Readonly: profileArgs(":read-only",
			"-c", `permissions.tandem.network.unix_sockets={"/h/herdr.sock"="allow"}`,
			"-c", proxyOn, "--ask-for-approval", "on-request"),
	}
	for autonomy, want := range cases {
		l := resolve(t, Spec{Kind: "codex", Role: RolePlanner, Autonomy: autonomy, HerdrSocket: "/h/herdr.sock", Cwd: "/repo"})
		if got := l.Args[:len(want)]; !reflect.DeepEqual(got, want) {
			t.Errorf("autonomy=%s:\n got %q\nwant %q", autonomy, l.Args, want)
		}
	}
}

// With no socket and no checkout known there is no socket to list, and the
// profile still keeps the internet shut.
func TestCodexSandboxProfileWithoutSockets(t *testing.T) {
	l := resolve(t, Spec{Kind: "codex", Role: RoleReviewer, Autonomy: Auto})
	want := profileArgs(":workspace", "-c", proxyOn, "--ask-for-approval", "never")
	if got := l.Args[:len(want)]; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", l.Args, want)
	}
}

// A user's grant becomes profile filesystem entries. A network grant opens the
// whole network, so the proxy is left off; Codex has no loopback-only grant
// beyond the one the profile already gives.
func TestCodexSandboxGrant(t *testing.T) {
	g := SandboxGrant{WritableDirs: []string{"/home/u/.gradle", "/opt/cache"}, Network: true}
	for _, autonomy := range []Autonomy{Ask, Auto} {
		l := resolve(t, Spec{Kind: "codex", Role: RoleReviewer, Autonomy: autonomy, Sandbox: g})
		fs := `permissions.tandem.filesystem={"/home/u/.gradle"="write","/opt/cache"="write"}`
		if !slices.Contains(l.Args, fs) {
			t.Errorf("autonomy=%s: got %q, want %s", autonomy, l.Args, fs)
		}
		if slices.Contains(l.Args, proxyOn) {
			t.Errorf("autonomy=%s: a network grant must leave the proxy off: %q", autonomy, l.Args)
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
			if a == `permissions.tandem.filesystem={"/home/u/.gradle"="write"}` {
				t.Errorf("autonomy=%s: got %v, want no grant", autonomy, l.Args)
			}
		}
		if !reflect.DeepEqual(l.Granted, SandboxGrant{}) {
			t.Errorf("autonomy=%s: granted %+v, want none", autonomy, l.Granted)
		}
	}
	if l := resolve(t, Spec{Kind: "codex", Role: RoleReviewer, Autonomy: Readonly, Sandbox: g}); !slices.Contains(l.Args, proxyOn) {
		t.Errorf("a read-only Codex ignores the network grant and keeps the proxy: %q", l.Args)
	}
}

// A path is written as a TOML basic string, so a quote or a backslash in it
// cannot end the string early.
func TestTomlString(t *testing.T) {
	cases := map[string]string{
		"/a b/c":    `"/a b/c"`,
		`/x"y`:      `"/x\"y"`,
		`C:\dir`:    `"C:\\dir"`,
		"tab\there": `"tab\u0009here"`,
	}
	for in, want := range cases {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s, want %s", in, got, want)
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
