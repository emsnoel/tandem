package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

// codexHome points CODEX_HOME at a temp dir. The real ~/.codex must never be
// touched: tests/run.sh is this repo's check_command and runs while a live
// builder holds that file.
func codexHome(t *testing.T, configBody string) string {
	t.Helper()
	home := t.TempDir()
	if configBody != "" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	return home
}

func find(actions []agent.Action, gate string) *agent.Action {
	for i := range actions {
		if actions[i].Gate == gate {
			return &actions[i]
		}
	}
	return nil
}

func TestCodexTrustAppliesAndMarks(t *testing.T) {
	home := codexHome(t, "")
	repo := t.TempDir()

	got := find(Check("codex", repo, agent.RoleBuilder), "project-trust")
	if got == nil {
		t.Fatal("no project-trust action")
	}
	if got.Disposition != agent.Apply || got.Status != agent.StatusApplied {
		t.Fatalf("got %+v, want Apply/Applied", got)
	}

	body, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), Marker) {
		t.Errorf("written entry must carry the marker; got:\n%s", body)
	}
	if !strings.Contains(string(body), `trust_level = "trusted"`) {
		t.Errorf("missing trust level; got:\n%s", body)
	}
}

func TestCodexTrustIsIdempotent(t *testing.T) {
	home := codexHome(t, "")
	repo := t.TempDir()

	Check("codex", repo, agent.RoleBuilder)
	first, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}

	got := find(Check("codex", repo, agent.RoleBuilder), "project-trust")
	if got.Status != agent.StatusCurrent {
		t.Fatalf("second call must report Current, got %v", got.Status)
	}
	second, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if string(first) != string(second) {
		t.Error("second call must not rewrite the file")
	}
}

func TestCodexTrustDryRunLeavesConfigByteIdentical(t *testing.T) {
	home := codexHome(t, "# existing\n")
	repo := t.TempDir()
	cfg := filepath.Join(home, "config.toml")
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}

	got := find(CheckWithOptions("codex", repo, agent.RoleBuilder, Options{DryRun: true}), "project-trust")
	if got == nil || got.Status != agent.StatusApplied {
		t.Fatalf("dry-run must report the action it would apply; got %+v", got)
	}
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("dry-run must leave config.toml byte-identical")
	}
}

// Entries written by the pre-marker codex_trust() must be recognised, left
// exactly as they are, and never duplicated.
func TestCodexTrustRecognisesUnmarkedLegacyEntry(t *testing.T) {
	repo := t.TempDir()
	key, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	legacy := "\n[projects.\"" + key + "\"]\ntrust_level = \"trusted\"\n"
	home := codexHome(t, legacy)

	got := find(Check("codex", repo, agent.RoleBuilder), "project-trust")
	if got.Status != agent.StatusCurrent {
		t.Fatalf("legacy entry must read as Current, got %v", got.Status)
	}
	body, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if string(body) != legacy {
		t.Errorf("legacy entry must be left byte-identical; got:\n%s", body)
	}
}

// Codex looks up the physical path, so the gate must too: an entry under an
// unresolved path is one Codex would not honour, and treating it as trusted
// would skip writing the entry the builder actually needs.
func TestCodexTrustIgnoresUnresolvedPathEntry(t *testing.T) {
	repo := t.TempDir()
	home := codexHome(t, "\n[projects.\""+repo+"\"]\ntrust_level = \"trusted\"\n")
	got := find(Check("codex", repo, agent.RoleBuilder), "project-trust")
	if got.Status != agent.StatusApplied {
		t.Fatalf("an unresolved-path entry must not count as trust; got %v", got.Status)
	}
	body, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if !strings.Contains(string(body), Marker) {
		t.Error("the gate should have written a real, marked entry")
	}
}

// A path containing a quote or backslash cannot be a TOML basic-string key.
// Refuse rather than emit a broken config.
func TestCodexTrustRefusesUnquotablePath(t *testing.T) {
	codexHome(t, "")
	bad := filepath.Join(t.TempDir(), `we"ird`)
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Skip("filesystem rejects the path; nothing to test")
	}
	got := find(Check("codex", bad, agent.RoleBuilder), "project-trust")
	if got.Status != agent.StatusManual {
		t.Fatalf("got %v, want Manual", got.Status)
	}
	if got.Remedy == "" {
		t.Error("a Manual status must carry a remedy")
	}
}

func TestCheckUnknownKindReturnsNothing(t *testing.T) {
	if a := Check("opencode", t.TempDir(), agent.RoleBuilder); len(a) != 0 {
		t.Fatalf("opencode has no launch-time gate; got %+v", a)
	}
}
