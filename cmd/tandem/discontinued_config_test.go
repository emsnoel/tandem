package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

const staleSupported = "supported kinds: claude, codex, copilot"

// staleManifests are the ways a .tandem.json written before the upgrade names
// OpenCode.
var staleManifests = map[string]struct{ manifest, role string }{
	"builder.kind":         {`{"builder": {"kind": "opencode"}, "reviewer": {"kind": "claude"}}`, "builder"},
	"top-level builder":    {`{"builder_kind": "opencode", "reviewer": {"kind": "claude"}}`, "builder"},
	"reviewer.kind":        {`{"builder": {"kind": "claude"}, "reviewer": {"kind": "opencode"}}`, "reviewer"},
	"builder and reviewer": {`{"builder": {"kind": "opencode"}, "reviewer": {"kind": "opencode"}}`, "builder"},
}

func launchSpec(t *testing.T, root string, extra ...string) (int, string) {
	t.Helper()
	rc, _, errOut := runCapture(t, append([]string{"agent", "launch-spec", "--fields", "--root", root, "--cwd", root}, extra...)...)
	return rc, errOut
}

// Following the old "approve it" advice led nowhere, so the kind is refused
// before the approval is even considered.
func TestStaleManifestIsRefusedBeforeApproval(t *testing.T) {
	for name, tc := range staleManifests {
		t.Run(name, func(t *testing.T) {
			root := doctorRepo(t, tc.manifest)
			rc, errOut := launchSpec(t, root, "--role", tc.role)
			if rc != exitcode.NarrowingRefused {
				t.Errorf("exit %d, want %d: %s", rc, exitcode.NarrowingRefused, errOut)
			}
			if strings.Contains(errOut, "approve-kind") {
				t.Errorf("sends the person to approve a dead kind: %s", errOut)
			}
			if !strings.Contains(errOut, `"opencode"`) || !strings.Contains(errOut, ".tandem.json") || !strings.Contains(errOut, staleSupported) {
				t.Errorf("does not say what is wrong: %s", errOut)
			}
		})
	}
}

func TestDoctorLeavesOneRefusalForAStaleManifest(t *testing.T) {
	for name, tc := range staleManifests {
		t.Run(name, func(t *testing.T) {
			root := doctorRepo(t, tc.manifest)
			out, rc := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root}) })
			if rc != exitcode.DoctorFailed {
				t.Errorf("exit %d, want %d\n%s", rc, exitcode.DoctorFailed, out)
			}
			for _, dead := range []string{"--approve-kind opencode", "install the opencode", "integration install opencode", "opencode not on PATH", "opencode 1.0"} {
				if strings.Contains(out, dead) {
					t.Errorf("doctor still says %q:\n%s", dead, out)
				}
			}
			var fails []string
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "  FAIL  ") && strings.Contains(l, "opencode") {
					fails = append(fails, l)
				}
			}
			want := strings.Count(tc.manifest, "opencode")
			if len(fails) != want {
				t.Errorf("%d refusal lines, want one for each of %d roles:\n%s", len(fails), want, out)
			}
			if !strings.Contains(out, staleSupported) {
				t.Errorf("no way forward:\n%s", out)
			}
		})
	}
}

func TestApproveKindRefusesAKindWithNoAdapter(t *testing.T) {
	for _, kind := range []string{"opencode", "banana"} {
		t.Run(kind, func(t *testing.T) {
			root := doctorRepo(t, `{}`)
			rc, _, errOut := runCapture(t, "config", "approve-kind", "--root", root, "--kind", kind)
			if rc != exitcode.NarrowingRefused {
				t.Errorf("exit %d, want %d: %s", rc, exitcode.NarrowingRefused, errOut)
			}
			if !strings.Contains(errOut, `"`+kind+`"`) || !strings.Contains(errOut, staleSupported) {
				t.Errorf("does not say what is wrong: %q", errOut)
			}
			if _, err := os.Stat(config.UserConfigPath()); err == nil {
				b, _ := os.ReadFile(config.UserConfigPath())
				t.Errorf("wrote the user config: %s", b)
			}
		})
	}
}

// The refusal names the kind, where it came from and what would work.
func TestLaunchRefusalNamesTheKindAndItsSource(t *testing.T) {
	type source struct {
		role     string
		manifest string
		setup    func(t *testing.T) (extra []string, want string)
	}
	for name, s := range map[string]source{
		"--builder": {"builder", `{}`, func(*testing.T) ([]string, string) { return []string{"--kind", "opencode"}, "--builder" }},
		"--reviewer": {"reviewer", `{"reviewer": {"kind": "claude"}}`, func(*testing.T) ([]string, string) {
			return []string{"--kind", "opencode"}, "--reviewer"
		}},
		"env": {"builder", `{}`, func(t *testing.T) ([]string, string) {
			t.Setenv("HERDR_BUILDER_KIND", "opencode")
			return nil, "HERDR_BUILDER_KIND"
		}},
		"user config": {"builder", `{}`, func(t *testing.T) ([]string, string) {
			p := config.UserConfigPath()
			if err := os.WriteFile(p, []byte(`{"defaults": {"builder_kind": "opencode"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil, p
		}},
		"approved manifest": {"builder", `{"builder": {"kind": "opencode"}}`, func(*testing.T) ([]string, string) { return nil, ".tandem.json" }},
	} {
		t.Run(name, func(t *testing.T) {
			root := doctorRepo(t, s.manifest)
			extra, want := s.setup(t)
			rc, errOut := launchSpec(t, root, append([]string{"--role", s.role}, extra...)...)
			if rc != exitcode.NarrowingRefused {
				t.Errorf("exit %d: %s", rc, errOut)
			}
			for _, part := range []string{`"opencode"`, s.role, want, staleSupported} {
				if !strings.Contains(errOut, part) {
					t.Errorf("missing %q in %q", part, errOut)
				}
			}
		})
	}
}

func TestRecordBuilderRefusesAKindWithNoAdapter(t *testing.T) {
	for name, args := range map[string][]string{
		"configured": {"--kind", "opencode"},
		"adopted":    {"--kind", "codex", "--adopted", "opencode"},
	} {
		t.Run(name, func(t *testing.T) {
			root := doctorRepo(t, `{}`)
			relay := t.TempDir()
			rc, _, errOut := runCapture(t, append([]string{"config", "record-builder", "--relay", relay, "--root", root}, args...)...)
			if rc != exitcode.NarrowingRefused {
				t.Errorf("exit %d: %s", rc, errOut)
			}
			if _, err := os.Stat(filepath.Join(relay, "builder.json")); err == nil {
				t.Error("builder.json was written")
			}
		})
	}
}

func TestCheckKindsAnswersForTheRecordedAgents(t *testing.T) {
	for name, tc := range map[string]struct {
		builder, reviewer string
		rc                int
		fields            []string
	}{
		"nothing recorded":     {"", "", exitcode.OK, nil},
		"both supported":       {"codex", "claude", exitcode.OK, nil},
		"opencode builder":     {"opencode", "claude", exitcode.NarrowingRefused, []string{"builder", "opencode", "claude, codex, copilot"}},
		"opencode reviewer":    {"codex", "opencode", exitcode.NarrowingRefused, []string{"reviewer", "opencode", "claude, codex, copilot"}},
		"the builder is first": {"banana", "opencode", exitcode.NarrowingRefused, []string{"builder", "banana", "claude, codex, copilot"}},
	} {
		t.Run(name, func(t *testing.T) {
			relay := t.TempDir()
			if tc.builder != "" {
				os.WriteFile(filepath.Join(relay, "builder.json"), []byte(`{"kind":"`+tc.builder+`","autonomy":"auto"}`), 0o644)
			}
			if tc.reviewer != "" {
				os.WriteFile(filepath.Join(relay, "reviewer.json"), []byte(`{"kind":"`+tc.reviewer+`","name":"reviewer"}`), 0o644)
			}
			out, rc := captureStdoutRC(t, func() int { return cmdConfig([]string{"check-kinds", "--relay", relay}) })
			if rc != tc.rc {
				t.Errorf("exit %d, want %d", rc, tc.rc)
			}
			if got := fields(out); strings.Join(got, "|") != strings.Join(tc.fields, "|") {
				t.Errorf("fields %q, want %q", got, tc.fields)
			}
		})
	}
}

func TestAgentSupportedAnswersWithAnExitCode(t *testing.T) {
	for kind, want := range map[string]int{"codex": exitcode.OK, "opencode": exitcode.NarrowingRefused} {
		if rc, out, errOut := runCapture(t, "agent", "supported", kind); rc != want || out != "" || errOut != "" {
			t.Errorf("%s: rc %d out %q err %q", kind, rc, out, errOut)
		}
	}
}

func TestAdoptableRefusesAnUnsupportedPaneRatherThanSkippingIt(t *testing.T) {
	const opencodeBeside = `{"result":{"agents":[
		{"pane_id":"w1:p1","agent":"claude","name":"planner","agent_status":"idle","tab_id":"w1:t1"},
		{"pane_id":"w1:p2","agent":"opencode","name":"","agent_status":"idle","tab_id":"w1:t1"}]}}`
	for name, extra := range map[string][]string{"no kind asked for": nil, "another kind asked for": {"--kind", "codex"}} {
		t.Run(name, func(t *testing.T) {
			stubHerdr(t, opencodeBeside)
			out, rc := captureStdoutRC(t, func() int {
				return cmdAgent(append([]string{"adoptable", "--exclude-pane", "w1:p1", "--tab", "w1:t1"}, extra...))
			})
			if got := fields(out); rc != exitcode.NarrowingRefused || len(got) != 2 || got[0] != "w1:p2" || got[1] != "opencode" {
				t.Fatalf("rc %d, fields %q", rc, got)
			}
		})
	}
	// A pane in another tab is none of this pair's business.
	stubHerdr(t, `{"result":{"agents":[{"pane_id":"w2:p2","agent":"opencode","name":"","agent_status":"idle","tab_id":"w2:t1"}]}}`)
	out, rc := captureStdoutRC(t, func() int {
		return cmdAgent([]string{"adoptable", "--exclude-pane", "w1:p1", "--tab", "w1:t1"})
	})
	if rc != exitcode.OK || out != "" {
		t.Fatalf("another tab: rc %d, out %q", rc, out)
	}
}
