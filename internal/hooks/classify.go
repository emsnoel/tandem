// Package hooks classifies Codex hook sources without deciding policy.
package hooks

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/paths"
	"strings"
)

// Finding is a hook source Codex has never reviewed, or one whose bytes have
// changed since the last clean classification.
type Finding struct {
	Kind string
	Path string
}

// Source pairs the key Codex stores in hooks.state with the hook file it loads.
type Source struct {
	Key  string
	Path string
}

// Sources returns the user hook file and hook files belonging to configured
// plugins. Unknown cache layouts and stale cache entries are ignored.
func Sources(codexHome string) []Source {
	cfg, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		// An unreadable config means no sources found; see Classify.
		return nil
	}

	var sources []Source
	user := filepath.Join(codexHome, "hooks.json")
	if regularFile(user) {
		sources = append(sources, Source{Key: user, Path: user})
	}

	pattern := filepath.Join(codexHome, "plugins", "cache", "*", "*", "*", "hooks", "hooks.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	for _, path := range files {
		if !regularFile(path) {
			continue
		}
		// Two levels up from the matched file is the plugin version directory.
		versionDir := filepath.Dir(filepath.Dir(path))
		plugin := filepath.Base(filepath.Dir(versionDir))
		marketplace := filepath.Base(filepath.Dir(filepath.Dir(versionDir)))
		if plugin == "" || marketplace == "" {
			continue
		}
		key := plugin + "@" + marketplace
		// Look for the section header instead of parsing the TOML.
		if !strings.Contains(string(cfg), `[plugins."`+key+`"]`) {
			continue
		}
		sources = append(sources, Source{Key: key, Path: path})
	}
	return sources
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// plausibleConfig does a shallow sanity check that b looks like TOML, without
// actually parsing it: reject anything containing a NUL byte, then require
// every non-blank, non-comment line to either be a `[section]` header or
// contain a `=`.
func plausibleConfig(b []byte) bool {
	if strings.IndexByte(string(b), 0) >= 0 {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") || strings.Contains(line, "=") {
			continue
		}
		return false
	}
	return true
}

func fileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// readBaseline loads a "hash  path" file, one entry per line, into a map
// from path to hash.
func readBaseline(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	baseline := make(map[string]string)
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.SplitN(s.Text(), "  ", 2) // split on the first double space only
		if len(parts) == 2 {
			baseline[parts[1]] = parts[0]
		}
	}
	if s.Err() != nil {
		return nil
	}
	return baseline
}

// Classify fails open: an unreadable or unrecognised input yields no findings.
// It does not attempt to reproduce Codex's private trusted_hash algorithm.
func Classify(codexHome string) []Finding {
	cfgPath := filepath.Join(codexHome, "config.toml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil || !plausibleConfig(cfg) {
		return nil
	}
	sources := Sources(codexHome)
	if len(sources) == 0 {
		return nil
	}

	baselinePath := paths.HookBaseline(codexHome)
	baseline := readBaseline(baselinePath)
	type row struct{ hash, path string }
	rows := make([]row, 0, len(sources))
	var findings []Finding
	for _, source := range sources {
		hash, err := fileHash(source.Path)
		if err != nil {
			return nil
		}
		rows = append(rows, row{hash: hash, path: source.Path})
		if !strings.Contains(string(cfg), `[hooks.state."`+source.Key+`:`) {
			findings = append(findings, Finding{Kind: "unreviewed", Path: source.Path})
		} else if old, ok := baseline[source.Path]; ok && old != hash {
			findings = append(findings, Finding{Kind: "changed", Path: source.Path})
		}
	}
	if len(findings) != 0 {
		return findings
	}

	// Write a temp file and rename it over the baseline, so a reader never sees a
	// partial file.
	tmp, err := os.CreateTemp(codexHome, filepath.Base(baselinePath)+".*")
	if err != nil {
		return nil
	}
	tmpPath := tmp.Name()
	// Removes the temp file if anything fails before the rename.
	defer os.Remove(tmpPath)
	for _, row := range rows {
		if _, err := fmt.Fprintf(tmp, "%s  %s\n", row.hash, row.path); err != nil {
			tmp.Close()
			return nil
		}
	}
	if err := tmp.Close(); err != nil {
		return nil
	}
	if err := os.Rename(tmpPath, baselinePath); err != nil {
		return nil
	}
	return nil
}
