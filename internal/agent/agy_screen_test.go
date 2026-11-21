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

func TestAgyFreshScreenReadsRealScreens(t *testing.T) {
	cases := []struct {
		file string
		want bool
	}{
		{"agy-main-fresh.txt", true},
		{"agy-main-after-new.txt", true},
		{"agy-main-after-turn.txt", false},
		{"agy-working.txt", false},
		{"agy-trust.txt", false},
		{"agy-permission.txt", false},
		{"agy-slash-menu.txt", false},
		{"claude-main.txt", false},
		{"codex-main.txt", false},
		{"copilot-main.txt", false},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			if got := FreshScreen("agy", agyScreen(t, c.file)); got != c.want {
				t.Errorf("FreshScreen(agy, %s) = %v, want %v", c.file, got, c.want)
			}
		})
	}
	// An unsubmitted /new sitting in the input box is not a completed reset.
	unsubmitted := strings.Replace(agyScreen(t, "agy-main-after-new.txt"), "\n>\n", "\n> /new\n", 1)
	if FreshScreen("agy", unsubmitted) {
		t.Errorf("unsubmitted /new in input box read as fresh")
	}
	// Only agy has a measured FreshScreen check.
	if FreshScreen("copilot", agyScreen(t, "agy-main-after-new.txt")) {
		t.Errorf("FreshScreen(copilot, ...) = true, want false")
	}
	if FreshScreen("agy", "") {
		t.Errorf("FreshScreen(agy, empty) = true, want false")
	}
}
