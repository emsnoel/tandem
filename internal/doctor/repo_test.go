package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/config"
)

// isolate points every config doctor reads at empty temp dirs, and returns a
// repo root holding manifest as its .tandem.json ("" writes none).
func isolate(t *testing.T, manifest string) string {
	t.Helper()
	for _, k := range []string{"HOME", "CODEX_HOME", "COPILOT_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	t.Setenv("HERDR_TANDEM_CONFIG", filepath.Join(t.TempDir(), "tandem.json"))
	for _, k := range []string{"HERDR_BUILDER_KIND", "HERDR_REVIEWER_KIND", "HERDR_STRICT_HOOK_CHECK"} {
		t.Setenv(k, "")
	}
	root := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(root, ".tandem.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// acceptClaude marks root as trusted in claude's own config, so claude's gate
// passes.
func acceptClaude(t *testing.T, root string) {
	t.Helper()
	key, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"projects":{"` + key + `":{"hasTrustDialogAccepted":true}}}`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repoResults(t *testing.T, root string, flags RoleFlags) []Result {
	t.Helper()
	roles, err := ResolveRoles(root, flags, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return Repo(root, root, roles, os.Getenv)
}

const codexRepo = `{"builder": {"kind": "codex"}, "reviewer": {"kind": "claude"}}`

func TestUnapprovedRepoKindFails(t *testing.T) {
	root := isolate(t, codexRepo)
	r := find(t, repoResults(t, root, RoleFlags{}), "approval:builder")
	if r.Status != Fail || r.Fix != "tandem-pair --approve-kind codex" || r.Group != "repo" {
		t.Fatalf("got %+v", r)
	}
}

func TestApprovedRepoKindPasses(t *testing.T) {
	root := isolate(t, codexRepo)
	if err := config.ApproveKind(os.Getenv("HERDR_TANDEM_CONFIG"), root, "codex"); err != nil {
		t.Fatal(err)
	}
	if r := find(t, repoResults(t, root, RoleFlags{}), "approval:builder"); r.Status != OK {
		t.Fatalf("got %+v", r)
	}
}

func TestUserChosenKindNeedsNoApproval(t *testing.T) {
	root := isolate(t, codexRepo)
	flags := RoleFlags{Builder: config.Overrides{Kind: "codex"}}
	if r := find(t, repoResults(t, root, flags), "approval:builder"); r.Status != OK {
		t.Fatalf("got %+v", r)
	}
}

func TestNoReviewerFails(t *testing.T) {
	root := isolate(t, `{"builder": {"kind": "codex"}}`)
	r := find(t, repoResults(t, root, RoleFlags{}), "reviewer")
	if r.Status != Fail || r.Fix != config.NoReviewerRemedy {
		t.Fatalf("got %+v", r)
	}
}

func TestInvalidReviewerFails(t *testing.T) {
	root := isolate(t, `{"builder": {"kind": "codex"}, "reviewer": {"model": "x"}}`)
	r := find(t, repoResults(t, root, RoleFlags{}), "reviewer")
	if r.Status != Fail || !strings.Contains(r.Detail, "sets no kind") {
		t.Fatalf("got %+v", r)
	}
}

func TestResolvedReviewerPasses(t *testing.T) {
	root := isolate(t, codexRepo)
	r := find(t, repoResults(t, root, RoleFlags{}), "reviewer")
	if r.Status != OK || !strings.Contains(r.Detail, "claude") {
		t.Fatalf("got %+v", r)
	}
}

// claude's trust dialog stalls an unattended builder, but the planner is a
// person at the keyboard.
func TestManualGateFailsForBuilderWarnsForPlanner(t *testing.T) {
	root := isolate(t, `{"builder": {"kind": "claude"}, "reviewer": {"kind": "codex"}}`)
	rs := repoResults(t, root, RoleFlags{Builder: config.Overrides{Kind: "claude"}})
	if r := find(t, rs, "trust:builder:project-trust"); r.Status != Fail || r.Fix == "" {
		t.Errorf("builder: got %+v", r)
	}
	if r := find(t, rs, "trust:planner:project-trust"); r.Status != Warn {
		t.Errorf("planner: got %+v", r)
	}
	acceptClaude(t, root)
	rs = repoResults(t, root, RoleFlags{Builder: config.Overrides{Kind: "claude"}})
	if r := find(t, rs, "trust:builder:project-trust"); r.Status != OK {
		t.Errorf("accepted builder: got %+v", r)
	}
}

// copilot's gate may write copilot's config when authorised, and codex's hook
// gate records a baseline once every hook is accepted. Doctor runs both dry,
// so nothing appears on disk.
func TestDryRunWritesNothing(t *testing.T) {
	root := isolate(t, codexRepo)
	codexHome := os.Getenv("CODEX_HOME")
	hooksFile := filepath.Join(codexHome, "hooks.json")
	cfg := `[hooks.state."` + hooksFile + `:session_start:0:0"]` + "\ntrusted_hash = \"sha256:aaa\"\n"
	for name, body := range map[string]string{"config.toml": cfg, "hooks.json": `{"hooks":{"SessionStart":[]}}`} {
		if err := os.WriteFile(filepath.Join(codexHome, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repoResults(t, root, RoleFlags{Builder: config.Overrides{Kind: "copilot"}, Reviewer: config.Overrides{Kind: "codex"}})
	for home, want := range map[string]int{os.Getenv("COPILOT_HOME"): 0, codexHome: 2} {
		entries, err := os.ReadDir(home)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != want {
			t.Errorf("doctor wrote into %s: %v", home, entries)
		}
	}
}

func TestStrictHookCheckFails(t *testing.T) {
	root := isolate(t, codexRepo)
	codexHome := os.Getenv("CODEX_HOME")
	for name, body := range map[string]string{"config.toml": "\n", "hooks.json": `{"hooks":{"SessionStart":[]}}`} {
		if err := os.WriteFile(filepath.Join(codexHome, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	flags := RoleFlags{Builder: config.Overrides{Kind: "codex"}}
	if r := find(t, repoResults(t, root, flags), "trust:builder:hook-acceptance"); r.Status != Warn {
		t.Fatalf("without strict mode an unaccepted hook warns: %+v", r)
	}
	t.Setenv("HERDR_STRICT_HOOK_CHECK", "1")
	if r := find(t, repoResults(t, root, flags), "trust:builder:hook-acceptance"); r.Status != Fail {
		t.Fatalf("strict mode: got %+v", r)
	}
}

// opencode cannot run read-only without widening to ask: exit 21 at launch.
func TestLaunchSettingRefusalFails(t *testing.T) {
	root := isolate(t, codexRepo)
	flags := RoleFlags{Builder: config.Overrides{Kind: "opencode", Autonomy: "readonly"}}
	r := find(t, repoResults(t, root, flags), "launch:builder")
	if r.Status != Fail || r.Fix == "" {
		t.Fatalf("got %+v", r)
	}
}

func TestLaunchSettingsPass(t *testing.T) {
	root := isolate(t, codexRepo)
	if r := find(t, repoResults(t, root, RoleFlags{}), "launch:reviewer"); r.Status != OK {
		t.Fatalf("got %+v", r)
	}
}

// Review focus 3: a broken manifest is an error the command turns into a fail.
func TestMalformedManifestIsAnError(t *testing.T) {
	root := isolate(t, `{"builder": `)
	if _, err := ResolveRoles(root, RoleFlags{}, os.Getenv); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
}

func TestUsesListsEveryRoleOnce(t *testing.T) {
	root := isolate(t, `{"builder": {"kind": "codex"}, "reviewer": {"kind": "codex"}}`)
	roles, err := ResolveRoles(root, RoleFlags{}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	u := roles.Uses()
	if len(u) != 2 || len(u["codex"]) != 2 || len(u["claude"]) != 1 {
		t.Fatalf("got %v", u)
	}
	if roles.Manifest != filepath.Join(root, ".tandem.json") {
		t.Fatalf("manifest %q", roles.Manifest)
	}
}
