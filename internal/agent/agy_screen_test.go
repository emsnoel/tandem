package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agyScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "screens", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAgyOffMainReadsRealScreens(t *testing.T) {
	cases := []struct {
		file string
		off  bool
		why  string
	}{
		{"agy-main-fresh.txt", false, ""},
		{"agy-main-after-turn.txt", false, ""},
		{"agy-main-after-new.txt", false, ""},
		{"agy-working.txt", false, ""},
		{"agy-trust.txt", true, "folder-trust"},
		{"agy-permission.txt", true, "permission"},
		{"agy-slash-menu.txt", true, "slash-command menu"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			why, off := OffMainInput("agy", agyScreen(t, c.file))
			if off != c.off || (c.off && !strings.Contains(why, c.why)) {
				t.Errorf("got off=%v why=%q, want off=%v why containing %q", off, why, c.off, c.why)
			}
		})
	}
}

func TestAgyOnlyTheBottomMarksADialog(t *testing.T) {
	lines := strings.Split(strings.TrimRight(agyScreen(t, "agy-main-after-turn.txt"), "\n"), "\n")
	at := len(lines) - 4 // above the rule, input, rule and footer
	quoted := append([]string{}, lines[:at]...)
	quoted = append(quoted, "  ↑/↓ Navigate · enter Confirm")
	quoted = append(quoted, lines[at:]...)
	if why, off := OffMainInput("agy", strings.Join(quoted, "\n")); off {
		t.Errorf("a quoted marker in the transcript made the screen off: %q", why)
	}
}

func TestAgyIgnoresOtherKindsMainScreens(t *testing.T) {
	for _, f := range []string{"copilot-main.txt", "claude-main.txt", "codex-main.txt"} {
		if why, off := OffMainInput("agy", agyScreen(t, f)); off {
			t.Errorf("%s: agy read it as off: %q", f, why)
		}
	}
}
