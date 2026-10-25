package trust

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/paths"
)

// codexConfigPath resolves Codex's config, honouring CODEX_HOME exactly as
// Codex itself does. Tests redirect it; the real file is never touched.
func codexConfigPath() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// projectHeader is the TOML table header Codex uses to record trust.
func projectHeader(key string) string { return `[projects."` + key + `"]` }

// hasProjectEntry reports whether the config already trusts key, whoever wrote
// the entry.
func hasProjectEntry(cfg, key string) bool {
	return strings.Contains(cfg, projectHeader(key))
}

// removeManagedEntry deletes only an entry tandem wrote, identified by the
// marker comment above its header. A human's entry is left alone. It returns
// the new content and whether anything was removed.
func removeManagedEntry(cfg, key string) (string, bool) {
	lines := strings.Split(cfg, "\n")
	header := projectHeader(key)
	for i, line := range lines {
		if strings.TrimSpace(line) != header {
			continue
		}
		if i == 0 || !strings.Contains(lines[i-1], Marker) {
			return cfg, false // not ours
		}
		// Drop the marker, the header, and the table's body up to the next
		// header or end of file.
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
			end++
		}
		out := append([]string{}, lines[:i-1]...)
		out = append(out, lines[end:]...)
		return strings.Join(out, "\n"), true
	}
	return cfg, false
}

// Clean removes the marked trust entry for cwd, if tandem wrote it.
// Reports whether anything was removed. An unmarked entry is never touched.
func Clean(kind, cwd string) (bool, error) {
	if kind == "copilot" {
		return cleanCopilot(cwd)
	}
	if kind != "codex" {
		return false, nil // no other kind has an Apply gate
	}
	path := codexConfigPath()
	if path == "" {
		return false, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		key = cwd
	}
	next, removed := removeManagedEntry(string(body), key)
	if !removed {
		return false, nil
	}
	// Write via a temp file and rename, so an interrupted clean cannot leave a
	// truncated config behind.
	tmp := path + ".tandem.tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// cleanCopilot removes cwd from Copilot's trustedFolders only if tandem's own
// ledger says tandem added it. JSON has no safe place for a marker, so the
// ledger stands in for Codex's TOML comment.
func cleanCopilot(cwd string) (bool, error) {
	home := paths.CopilotHome()
	if home == "" {
		return false, nil
	}
	ledgerPath := paths.CopilotTrustLedger(home)
	ledger := readLedger(ledgerPath)
	if len(ledger) == 0 {
		return false, nil
	}

	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		key = cwd
	}
	if !containsPath(ledger, key) && !containsPath(ledger, cwd) {
		return false, nil // not ours
	}

	path := filepath.Join(home, "config.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	banner, doc, trusted, err := parseCopilotConfig(body)
	if err != nil {
		return false, err
	}

	kept := make([]string, 0, len(trusted))
	removed := false
	for _, f := range trusted {
		if f == key || f == cwd {
			removed = true
			continue
		}
		kept = append(kept, f)
	}
	if !removed {
		return false, nil
	}
	if err := writeCopilotConfig(path, banner, doc, kept); err != nil {
		return false, err
	}

	// Drop it from the ledger too, so a later clean does not claim a folder a
	// human has since re-trusted by hand.
	var rest []string
	for _, l := range ledger {
		if l != key && l != cwd {
			rest = append(rest, l)
		}
	}
	body = []byte(strings.Join(rest, "\n"))
	if len(rest) > 0 {
		body = append(body, '\n')
	}
	if err := os.WriteFile(ledgerPath, body, 0o600); err != nil {
		return true, err // the trust IS removed; only the ledger is stale
	}
	return true, nil
}

func containsPath(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
