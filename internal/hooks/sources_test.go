package hooks

import (
	"path/filepath"
	"strings"
	"testing"
)

const oneHook = `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`

// pluginHome gives a CODEX_HOME with one enabled plugin, plug@mkt, that has a
// hooks/hooks.json with one hook and the given .codex-plugin/plugin.json. An
// empty manifest writes none. Nothing is accepted.
func pluginHome(t *testing.T, manifest string) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "plugins", "cache", "mkt", "plug", "1.0.0")
	writeFixture(t, filepath.Join(dir, "hooks", "hooks.json"), oneHook)
	if manifest != "" {
		writeFixture(t, filepath.Join(dir, ".codex-plugin", "plugin.json"), manifest)
	}
	writeFixture(t, filepath.Join(home, "config.toml"), `[plugins."plug@mkt"]`+"\nenabled = true\n")
	return home, dir
}

// superpowers 7.0.2 ships a hooks/hooks.json for other CLIs and tells Codex it
// has no hooks. Codex never asks about it and records no state for it, so a
// finding there could never be cleared (measured 2026-11-14, F31).
func TestAPluginWhoseCodexManifestDeclaresNoHooksIsNotASource(t *testing.T) {
	home, _ := pluginHome(t, `{"name":"plug","hooks":{}}`)
	if got := Sources(home); len(got) != 0 {
		t.Fatalf("want no sources, got %+v", got)
	}
	if got := Classify(home); len(got) != 0 {
		t.Fatalf("want no findings, got %+v", got)
	}
}

func TestAPluginWithNoCodexManifestUsesItsDefaultHookFile(t *testing.T) {
	for _, manifest := range []string{"", `{"name":"plug"}`} {
		home, dir := pluginHome(t, manifest)
		got := finding(t, Classify(home))
		if got.Kind != "unreviewed" || got.Path != filepath.Join(dir, "hooks", "hooks.json") {
			t.Errorf("manifest %q: got %+v", manifest, got)
		}
	}
}

// remember 0.43.1 points Codex at hooks/hooks.codex.json. That file is the
// source, not the hooks/hooks.json beside it.
func TestAManifestPathNamesTheHookFile(t *testing.T) {
	home, dir := pluginHome(t, `{"hooks":"./hooks/hooks.codex.json"}`)
	if got := Sources(home); len(got) != 0 {
		t.Fatalf("the named file does not exist yet, so there is no source: %+v", got)
	}
	named := filepath.Join(dir, "hooks", "hooks.codex.json")
	writeFixture(t, named, oneHook)
	if got := finding(t, Classify(home)); got.Path != named {
		t.Fatalf("want %s, got %+v", named, got)
	}
}

// A manifest path must stay inside the plugin.
func TestAManifestPathOutsideThePluginIsNotASource(t *testing.T) {
	home, _ := pluginHome(t, `{"hooks":"../../../../../hooks.json"}`)
	writeFixture(t, filepath.Join(home, "hooks.json"), oneHook)
	for _, s := range Sources(home) {
		if s.Key == "plug@mkt" {
			t.Fatalf("a path outside the plugin became its source: %+v", s)
		}
	}
}

// The bundled browser plugin declares its hooks inside plugin.json.
func TestInlineManifestHooksMakeTheManifestTheSource(t *testing.T) {
	home, dir := pluginHome(t, `{"hooks":{"hooks":{"Stop":[{"hooks":[{"type":"mcp_tool","tool":"x"}]}]}}}`)
	got := finding(t, Classify(home))
	if got.Path != filepath.Join(dir, ".codex-plugin", "plugin.json") {
		t.Fatalf("got %+v", got)
	}
}

// A hook file with no hook in it gives Codex nothing to accept.
func TestAHookFileWithNoHooksIsNotASource(t *testing.T) {
	for _, body := range []string{`{}`, `{"hooks":{}}`, `{"hooks":{"Stop":[]}}`, `{"hooks":{"Stop":[{"hooks":[]}]}}`, `not json`} {
		home, dir := pluginHome(t, "")
		writeFixture(t, filepath.Join(dir, "hooks", "hooks.json"), body)
		writeFixture(t, filepath.Join(home, "hooks.json"), body)
		if got := Sources(home); len(got) != 0 {
			t.Errorf("%s: want no sources, got %+v", strings.TrimSpace(body), got)
		}
	}
}
