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
		{"agy-feedback-survey.txt", true, "feedback survey"},
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
		{"agy-feedback-survey.txt", false},
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

// agy's feedback survey takes the place of the input box after some turns and
// drops whatever is typed (#118). Its Skip key dismisses it.
func TestAgyNoticeKeyDismissesOnlyTheSurvey(t *testing.T) {
	survey := agyScreen(t, "agy-feedback-survey.txt")
	if got := NoticeKey("agy", survey); got != "0" {
		t.Errorf("NoticeKey(agy, survey) = %q, want \"0\" (its Skip)", got)
	}
	for _, f := range []string{"agy-main-fresh.txt", "agy-main-after-turn.txt", "agy-working.txt", "agy-trust.txt", "agy-permission.txt", "agy-slash-menu.txt"} {
		if got := NoticeKey("agy", agyScreen(t, f)); got != "" {
			t.Errorf("NoticeKey(agy, %s) = %q, want none", f, got)
		}
	}
	for _, kind := range []string{"claude", "codex", "copilot"} {
		if got := NoticeKey(kind, survey); got != "" {
			t.Errorf("NoticeKey(%s, agy's survey) = %q, want none", kind, got)
		}
	}
}

// A transcript that quotes the survey above a live input box is not the survey.
func TestAgySurveyCountsOnlyAtTheBottom(t *testing.T) {
	lines := strings.Split(strings.TrimRight(agyScreen(t, "agy-main-after-turn.txt"), "\n"), "\n")
	at := len(lines) - 4 // above the rule, input, rule and footer
	quoted := append([]string{}, lines[:at]...)
	quoted = append(quoted, " How's the CLI experience so far? Help us improve:", " [1] Good  [2] Fine  [3] Bad  [0] Skip")
	quoted = append(quoted, lines[at:]...)
	screen := strings.Join(quoted, "\n")
	if got := NoticeKey("agy", screen); got != "" {
		t.Errorf("a quoted survey got key %q", got)
	}
	if why, off := OffMainInput("agy", screen); off {
		t.Errorf("a quoted survey made the screen off: %q", why)
	}
}
