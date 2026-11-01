package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

const rulingID = "20261101-101530-4821-19273"

func sidecar(t *testing.T, body string) string {
	t.Helper()
	relay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(relay, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(relay, "reviews", rulingID+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return relay
}

func TestGateRulingClean(t *testing.T) {
	relay := sidecar(t, `{"verdict":"clean","override_reason":"old"}`)
	out := captureStdout(t, func() int { return cmdGate([]string{"ruling", "--relay", relay, "--id", rulingID}) })
	if out != "clean\x00\x00" {
		t.Errorf("got %q: a ruling never stands on a clean review", out)
	}
}

func TestGateRulingStanding(t *testing.T) {
	relay := sidecar(t, `{"verdict":"objections","override_reason":"the plan is right"}`)
	out := captureStdout(t, func() int { return cmdGate([]string{"ruling", "--relay", relay, "--id", rulingID}) })
	if out != "objections\x00the plan is right\x00" {
		t.Errorf("got %q", out)
	}
}

func TestGateRulingMissingSidecar(t *testing.T) {
	relay := sidecar(t, "")
	out := captureStdout(t, func() int { return cmdGate([]string{"ruling", "--relay", relay, "--id", rulingID}) })
	if out != "\x00\x00" {
		t.Errorf("got %q", out)
	}
}

func TestGateRulingBadID(t *testing.T) {
	_, rc := captureStdoutRC(t, func() int { return cmdGate([]string{"ruling", "--relay", t.TempDir(), "--id", "../x"}) })
	if rc != exitcode.Usage {
		t.Errorf("rc %d, want usage", rc)
	}
}

// Review focus 5: the write keeps every key it does not know.
func TestGateRuleKeepsOtherKeys(t *testing.T) {
	relay := sidecar(t, `{"verdict":"objections","plan":"/p.md","extra":{"a":[1]}}`)
	captureStdout(t, func() int {
		return cmdGate([]string{"rule", "--relay", relay, "--id", rulingID, "--because", "ruled"})
	})
	b, err := os.ReadFile(filepath.Join(relay, "reviews", rulingID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"verdict": "objections", "plan": "/p.md", "extra": map[string]any{"a": []any{1.0}}, "override_reason": "ruled"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(relay, "reviews", rulingID+".json.tmp")); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind")
	}
}

func TestGateRuleRefusesNonObject(t *testing.T) {
	relay := sidecar(t, `[1]`)
	_, rc := captureStdoutRC(t, func() int {
		return cmdGate([]string{"rule", "--relay", relay, "--id", rulingID, "--because", "ruled"})
	})
	if rc != exitcode.Internal {
		t.Errorf("rc %d, want internal", rc)
	}
}

func TestConfigBuilder(t *testing.T) {
	relay := t.TempDir()
	if err := os.WriteFile(filepath.Join(relay, "builder.json"), []byte(`{"kind":"codex","autonomy":"auto","adopted":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, func() int { return cmdConfig([]string{"builder", "--relay", relay}) }); out != "codex\x00auto\x00true\x00" {
		t.Errorf("got %q", out)
	}
}

// No readable record prints empty kind and autonomy, so the caller's own
// check fires, and adopted false.
func TestConfigBuilderMissing(t *testing.T) {
	out, rc := captureStdoutRC(t, func() int { return cmdConfig([]string{"builder", "--relay", t.TempDir()}) })
	if rc != exitcode.OK || out != "\x00\x00false\x00" {
		t.Errorf("rc %d, got %q", rc, out)
	}
}

func TestConfigBuilderNotJSON(t *testing.T) {
	relay := t.TempDir()
	if err := os.WriteFile(filepath.Join(relay, "builder.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, rc := captureStdoutRC(t, func() int { return cmdConfig([]string{"builder", "--relay", relay}) })
	if rc != exitcode.OK || out != "\x00\x00false\x00" {
		t.Errorf("rc %d, got %q", rc, out)
	}
}

func TestConfigBuilderKindIsGone(t *testing.T) {
	if _, rc := captureStdoutRC(t, func() int { return cmdConfig([]string{"builder-kind", "--relay", t.TempDir()}) }); rc != exitcode.Usage {
		t.Errorf("rc %d, want usage", rc)
	}
}

func TestGateRuleKeepsHTMLCharacters(t *testing.T) {
	relay := sidecar(t, `{"verdict":"objections","plan":"/a&b<c>.md"}`)
	captureStdout(t, func() int {
		return cmdGate([]string{"rule", "--relay", relay, "--id", rulingID, "--because", "x && y"})
	})
	b, _ := os.ReadFile(filepath.Join(relay, "reviews", rulingID+".json"))
	if !strings.Contains(string(b), `"/a&b<c>.md"`) || !strings.Contains(string(b), `"x && y"`) {
		t.Errorf("got %s", b)
	}
}
