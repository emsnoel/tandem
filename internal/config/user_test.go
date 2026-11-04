package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUser(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tandem.json")
	body := `{
	  "defaults": {"builder_kind": "copilot"},
	  "adapters": {
	    "codex":    {"path": "/opt/homebrew/bin/codex",
	                 "args": ["-c", "model_reasoning_effort=high"]},
	    "copilot": {"allow_widen": ["autonomy"]}
	  }
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUser(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Defaults.BuilderKind != "copilot" {
		t.Errorf("got %q", u.Defaults.BuilderKind)
	}
	if u.Adapters["codex"].Path != "/opt/homebrew/bin/codex" {
		t.Errorf("got %q", u.Adapters["codex"].Path)
	}
	if len(u.Adapters["codex"].Args) != 2 {
		t.Errorf("got %v", u.Adapters["codex"].Args)
	}
	if len(u.Adapters["copilot"].AllowWiden) != 1 {
		t.Errorf("got %v", u.Adapters["copilot"].AllowWiden)
	}
}

func TestLoadUserMissingFileIsNotAnError(t *testing.T) {
	u, err := LoadUser(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || u == nil {
		t.Fatalf("got (%v, %v), want an empty User and no error", u, err)
	}
	if u.Adapters == nil {
		t.Fatal("Adapters must be non-nil so callers can index it freely")
	}
}

func TestLoadUserEmptyFileIsEmptyConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tandem.json")
	if err := os.WriteFile(p, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUser(p)
	if err != nil {
		t.Fatalf("an empty file must read as empty config, not an error: %v", err)
	}
	if u.Adapters == nil {
		t.Fatal("Adapters must be non-nil")
	}
}

func TestUserConfigPathHonoursOverride(t *testing.T) {
	t.Setenv("HERDR_TANDEM_CONFIG", "/custom/tandem.json")
	if got := UserConfigPath(); got != "/custom/tandem.json" {
		t.Fatalf("got %q", got)
	}
}
