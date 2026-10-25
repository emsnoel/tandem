// Package paths resolves the files tandem keeps in a repository and in other
// tools' home directories.
//
// Each name has a legacy herdr- spelling from before the rename. Resolution
// prefers the current name and falls back to the legacy one only when it alone
// exists. Files are never migrated, because a session in flight may hold them.
// The bash entry points use the same rule through `tandem paths`.
package paths

import (
	"os"
	"path/filepath"
)

const (
	// Manifest is the per-repo configuration tandem-init writes.
	ManifestName       = ".tandem.json"
	legacyManifestName = ".herdr.json"

	// Relay is the mailbox directory carrying questions, answers and reports
	// between the two agents.
	RelayName       = ".tandem-relay"
	legacyRelayName = ".herdr-relay"

	// HookBaseline is the copy of Codex's hook configuration tandem records in
	// $CODEX_HOME to detect later edits.
	HookBaselineName       = ".tandem-hook-baseline"
	legacyHookBaselineName = ".herdr-hook-baseline"

	// CopilotTrustLedgerName records the folders tandem added to Copilot's
	// trustedFolders, so a teardown removes only those.
	CopilotTrustLedgerName = ".tandem-trusted"
)

// IsRelayName reports whether name is a relay directory name, current or
// legacy.
func IsRelayName(name string) bool { return name == RelayName || name == legacyRelayName }

// present reports whether anything occupies p. It uses Lstat so a broken
// symlink counts as present and the next read fails loudly.
func present(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// pick resolves one name under dir, preferring the current name and falling
// back to the legacy one only when it alone exists. With neither present it
// returns the current name, which is what a writer should create.
func pick(dir, name, legacy string) string {
	current := filepath.Join(dir, name)
	if present(current) {
		return current
	}
	if old := filepath.Join(dir, legacy); present(old) {
		return old
	}
	return current
}

// Manifest resolves the per-repo configuration file under root.
func Manifest(root string) string { return pick(root, ManifestName, legacyManifestName) }

// NearestManifest walks up from from to boundary, both inclusive, and returns
// the first existing manifest. boundary is from or an ancestor, usually the git
// toplevel. With no manifest found it returns Manifest(boundary).
//
// Both paths are resolved through symlinks first, because git reports a
// physical toplevel and a logical from would never compare equal to it.
func NearestManifest(from, boundary string) string {
	if resolved, err := filepath.EvalSymlinks(from); err == nil {
		from = resolved
	}
	if resolved, err := filepath.EvalSymlinks(boundary); err == nil {
		boundary = resolved
	}
	dir := from
	for {
		if candidate := pick(dir, ManifestName, legacyManifestName); present(candidate) {
			return candidate
		}
		if dir == boundary {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return Manifest(boundary)
}

// Relay resolves the mailbox directory under root.
func Relay(root string) string { return pick(root, RelayName, legacyRelayName) }

// HookBaseline resolves the recorded Codex hook baseline under codexHome.
func HookBaseline(codexHome string) string {
	return pick(codexHome, HookBaselineName, legacyHookBaselineName)
}

// CodexHome locates Codex's home directory: $CODEX_HOME, else ~/.codex.
// It returns "" when neither is available, and callers must report that.
func CodexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".codex")
	}
	return ""
}

// CopilotHome locates Copilot's home directory: $COPILOT_HOME, else ~/.copilot.
// It returns "" when neither is available, and callers must report that.
func CopilotHome() string {
	if h := os.Getenv("COPILOT_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".copilot")
	}
	return ""
}

// CopilotTrustLedger resolves tandem's record of the folders it trusted on
// Copilot's behalf.
func CopilotTrustLedger(copilotHome string) string {
	return filepath.Join(copilotHome, CopilotTrustLedgerName)
}
