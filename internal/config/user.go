package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// UserAdapter holds per-adapter settings. Every field here is capability-
// bearing or adapter-specific, which is why none of it may come from a repo.
type UserAdapter struct {
	Path         string   `json:"path"`
	Args         []string `json:"args"`
	AllowWiden   []string `json:"allow_widen"`
	AllowDegrade []string `json:"allow_degrade"`
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	Autonomy     string   `json:"autonomy"`
}

type UserDefaults struct {
	PlannerKind string `json:"planner_kind"`
	BuilderKind string `json:"builder_kind"`
}

type User struct {
	Defaults UserDefaults           `json:"defaults"`
	Adapters map[string]UserAdapter `json:"adapters"`
	// Approvals records which agent kinds this user has accepted from a given
	// repository's own config, keyed by absolute repository path. A kind the
	// user supplied themselves is never listed here: it needs no approval.
	Approvals map[string][]string `json:"approvals"`
}

// UserConfigPath resolves the user config location. HERDR_TANDEM_CONFIG
// overrides it, which is also how tests avoid touching a real home directory.
func UserConfigPath() string {
	if p := os.Getenv("HERDR_TANDEM_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "tandem.json"
	}
	return filepath.Join(home, ".config", "herdr", "tandem.json")
}

// LoadUser reads the user config. A missing file is not an error. Adapters is
// always non-nil so callers may index it without a check.
func LoadUser(path string) (*User, error) {
	u := &User{Adapters: map[string]UserAdapter{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return u, nil
	}
	if err != nil {
		return nil, err
	}
	// An empty or whitespace-only file means "no user config", not a parse
	// error. A truncated file is a plausible accident and must not block a
	// pairing when the effect is identical to the file being absent.
	if len(bytes.TrimSpace(b)) == 0 {
		return u, nil
	}
	if err := json.Unmarshal(b, u); err != nil {
		return nil, err
	}
	if u.Adapters == nil {
		u.Adapters = map[string]UserAdapter{}
	}
	return u, nil
}
