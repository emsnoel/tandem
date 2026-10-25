package trust

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

func claudeHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	return home
}

func TestClaudeTrustAccepted(t *testing.T) {
	repo := t.TempDir()
	key, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	claudeHome(t, `{"projects":{"`+key+`":{"hasTrustDialogAccepted":true}}}`)

	got := find(Check("claude", repo, agent.RoleBuilder), "project-trust")
	if got == nil {
		t.Fatal("no project-trust action")
	}
	if got.Disposition != agent.Report {
		t.Errorf("must never write ~/.claude.json; got %v", got.Disposition)
	}
	if got.Status != agent.StatusCurrent {
		t.Fatalf("got %v, want Current", got.Status)
	}
}

func TestClaudeTrustNotAcceptedIsManual(t *testing.T) {
	repo := t.TempDir()
	claudeHome(t, `{"projects":{}}`)

	got := find(Check("claude", repo, agent.RoleBuilder), "project-trust")
	if got.Status != agent.StatusManual {
		t.Fatalf("got %v, want Manual", got.Status)
	}
	if got.Remedy == "" {
		t.Error("a Manual status must carry a remedy")
	}
}

func TestClaudeTrustNeverWrites(t *testing.T) {
	repo := t.TempDir()
	home := claudeHome(t, `{"projects":{}}`)
	p := filepath.Join(home, ".claude.json")
	before, _ := os.ReadFile(p)

	Check("claude", repo, agent.RoleBuilder)

	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("~/.claude.json must be byte-identical after a check")
	}
}

// A missing file is not an error: Claude Code has simply never run here.
func TestClaudeTrustMissingFileIsManualNotFailed(t *testing.T) {
	repo := t.TempDir()
	claudeHome(t, "")
	got := find(Check("claude", repo, agent.RoleBuilder), "project-trust")
	if got.Status != agent.StatusManual {
		t.Fatalf("got %v, want Manual", got.Status)
	}
}
