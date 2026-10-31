package herdr

import "testing"

func TestParseVersion(t *testing.T) {
	got, err := ParseVersion("herdr 0.9.3\n")
	if err != nil || got != "0.9.3" {
		t.Fatalf("got %q, %v; want 0.9.3", got, err)
	}
	if _, err := ParseVersion("garbage"); err == nil {
		t.Fatal("output with no version must be an error")
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		have string
		want bool
	}{
		{"0.9.3", true},
		{"0.10.0", true},
		{"1.0", true},
		{"0.9.2", false},
		{"0.8.9", false},
	}
	for _, c := range cases {
		if got := VersionAtLeast(c.have, MinVersion); got != c.want {
			t.Errorf("VersionAtLeast(%q, %q) = %v, want %v", c.have, MinVersion, got, c.want)
		}
	}
}

// The fixture is `herdr integration status` from a real machine, home path
// shortened.
const integrationFixture = `claude: outdated (v8 < v10) (/Users/x/.claude/hooks/herdr-agent-state.sh)
codex: current (v8) (/Users/x/.codex/herdr-agent-state.sh)
opencode: not installed (/Users/x/.config/opencode/plugins/herdr-agent-state.js)
letta (experimental): not installed (/Users/x/.letta/hooks/herdr-agent-session.sh)
`

func TestParseIntegrationStatus(t *testing.T) {
	got := ParseIntegrationStatus(integrationFixture)
	want := map[string]Integration{
		"claude":   {Kind: "claude", State: "outdated", Detail: "v8 < v10"},
		"codex":    {Kind: "codex", State: "current", Detail: "v8"},
		"opencode": {Kind: "opencode", State: "not installed"},
		"letta":    {Kind: "letta", State: "not installed"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %+v, want %+v", k, got[k], w)
		}
	}
}
