package agent

import "testing"

func TestFirstUnsupported(t *testing.T) {
	for _, tc := range []struct {
		kinds []string
		want  string
	}{
		{nil, ""},
		{[]string{"", ""}, ""},
		{[]string{"codex", "claude"}, ""},
		{[]string{"", "opencode"}, "opencode"},
		{[]string{"codex", "banana", "opencode"}, "banana"},
	} {
		if got := FirstUnsupported(tc.kinds...); got != tc.want {
			t.Errorf("FirstUnsupported(%q) = %q, want %q", tc.kinds, got, tc.want)
		}
	}
}
