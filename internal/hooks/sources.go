package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Source pairs the key Codex stores in hooks.state with the file that declares
// the hooks.
type Source struct {
	Key  string
	Path string
}

// Sources returns the user hook file and the hook declarations of configured
// plugins, as Codex loads them. A declaration with no hook in it is not a
// source: Codex has nothing to accept there and records no state for it, so a
// finding could never be cleared. Unknown cache layouts are ignored.
func Sources(codexHome string) []Source {
	cfg, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		// An unreadable config means no sources found; see Classify.
		return nil
	}

	var sources []Source
	user := filepath.Join(codexHome, "hooks.json")
	if declaresHooks(user) {
		sources = append(sources, Source{Key: user, Path: user})
	}

	// cache/<marketplace>/<plugin>/<version>
	dirs, err := filepath.Glob(filepath.Join(codexHome, "plugins", "cache", "*", "*", "*"))
	if err != nil {
		return nil
	}
	for _, dir := range dirs {
		plugin := filepath.Base(filepath.Dir(dir))
		marketplace := filepath.Base(filepath.Dir(filepath.Dir(dir)))
		key := plugin + "@" + marketplace
		// Look for the section header instead of parsing the TOML.
		if !strings.Contains(string(cfg), `[plugins."`+key+`"]`) {
			continue
		}
		if path := pluginHooks(dir); path != "" {
			sources = append(sources, Source{Key: key, Path: path})
		}
	}
	return sources
}

// pluginHooks returns the file in which the plugin at dir declares hooks to
// Codex, or "" when it declares none. The "hooks" key of
// .codex-plugin/plugin.json decides, in the three forms seen in installed
// plugins (2026-11-14):
//
//	absent, or no manifest        hooks/hooks.json            (expo, warp)
//	"./hooks/hooks.codex.json"    that file                   (remember)
//	{"hooks": {...}} or {}        the manifest itself, or none (browser, superpowers)
//
// Any other shape is unknown and yields no source, as Classify fails open.
func pluginHooks(dir string) string {
	manifest := filepath.Join(dir, ".codex-plugin", "plugin.json")
	fallback := filepath.Join(dir, "hooks", "hooks.json")

	var m map[string]json.RawMessage
	raw, err := os.ReadFile(manifest)
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return ifDeclares(fallback)
	}
	decl, ok := m["hooks"]
	if !ok {
		return ifDeclares(fallback)
	}

	var rel string
	if json.Unmarshal(decl, &rel) == nil {
		path := filepath.Join(dir, rel)
		// A path that leaves the plugin is not this plugin's hook file.
		if r, err := filepath.Rel(dir, path); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return ""
		}
		return ifDeclares(path)
	}
	if hasHandlers(decl) {
		return manifest
	}
	return ""
}

func ifDeclares(path string) string {
	if declaresHooks(path) {
		return path
	}
	return ""
}

// declaresHooks reports whether path is a regular file holding at least one hook.
func declaresHooks(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	raw, err := os.ReadFile(path)
	return err == nil && hasHandlers(raw)
}

// hasHandlers reports whether a hooks document, {"hooks": {event: [group]}},
// has a group with at least one hook in it.
func hasHandlers(raw []byte) bool {
	var doc struct {
		Hooks map[string][]struct {
			Hooks []json.RawMessage `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for _, groups := range doc.Hooks {
		for _, g := range groups {
			if len(g.Hooks) > 0 {
				return true
			}
		}
	}
	return false
}
