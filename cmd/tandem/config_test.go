package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/paths"
)

// withFixture points both config sources at temp files and returns the repo dir.
func withFixture(t *testing.T, repoJSON, userJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if repoJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, paths.ManifestName), []byte(repoJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	up := filepath.Join(dir, "tandem.json")
	if err := os.WriteFile(up, []byte(userJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TANDEM_CONFIG", up)
	return dir
}

func TestLaunchSpecEmitsArgv(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","model":"gpt-5.4","autonomy":"auto"}}`, "")
	if err := config.ApproveKind(config.UserConfigPath(), dir, "codex"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int {
		return cmdLaunchSpec([]string{"--role", "builder", "--root", dir, "--cwd", dir})
	})
	var got struct {
		Kind string   `json:"kind"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Kind != "codex" {
		t.Errorf("got kind %q", got.Kind)
	}
	if len(got.Args) == 0 || got.Args[0] != "--sandbox" {
		t.Errorf("got args %v", got.Args)
	}
}

func TestLaunchSpecRefusalUsesTheRightExitCode(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"opencode","autonomy":"readonly"}}`, "")
	if err := config.ApproveKind(config.UserConfigPath(), dir, "opencode"); err != nil {
		t.Fatal(err)
	}
	rc := cmdLaunchSpec([]string{"--role", "builder", "--root", dir, "--cwd", dir})
	if rc != 21 {
		t.Fatalf("got exit %d, want 21 (widening refused)", rc)
	}
}

// A kind the REPOSITORY asked for is confirmed once per repository.
func TestLaunchSpecRefusesUnapprovedRepoKind(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"opencode","autonomy":"full"}}`, "")
	rc := cmdLaunchSpec([]string{"--role", "builder", "--root", dir, "--cwd", dir})
	if rc != 22 {
		t.Fatalf("got exit %d, want 22", rc)
	}
}

// The gate keys on PROVENANCE, not on the value: a kind the user supplied
// themselves is already their decision and must never be challenged. Same
// kind, same repo, no approval -- but it comes from a flag, so it proceeds.
func TestLaunchSpecDoesNotChallengeUserSuppliedKind(t *testing.T) {
	dir := withFixture(t, `{}`, "")
	rc := cmdLaunchSpec([]string{"--role", "builder", "--root", dir,
		"--cwd", dir, "--kind", "opencode", "--autonomy", "full"})
	if rc != 0 {
		t.Fatalf("got exit %d, want 0: a user-supplied kind needs no approval", rc)
	}
}

func TestConfigResolveReportsProvenance(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex","model":"repo-model"}}`, "")
	t.Setenv("HERDR_BUILDER_MODEL", "env-model")
	out := captureStdout(t, func() int {
		return cmdConfig([]string{"resolve", "--role", "builder", "--root", dir})
	})
	var got struct {
		Model struct {
			Value string `json:"value"`
			From  string `json:"from"`
		} `json:"model"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Model.Value != "env-model" || got.Model.From != "env" {
		t.Fatalf("got %+v", got.Model)
	}
}

func captureStdout(t *testing.T, fn func() int) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	rc := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	if rc != 0 {
		t.Fatalf("command exited %d: %s", rc, b)
	}
	return string(b)
}

// A repo paired before the rename holds a .herdr.json and no .tandem.json. The
// resolve path must read it, or an existing repo's builder configuration is
// silently ignored and the pairing falls back to defaults.
func TestResolveReadsThePreRenameManifest(t *testing.T) {
	dir := t.TempDir()
	body := `{"builder":{"kind":"codex","model":"gpt-5.4","autonomy":"auto"}}`
	if err := os.WriteFile(filepath.Join(dir, ".herdr.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join(dir, "tandem.json")
	if err := os.WriteFile(up, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TANDEM_CONFIG", up)
	if err := config.ApproveKind(config.UserConfigPath(), dir, "codex"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int {
		return cmdLaunchSpec([]string{"--role", "builder", "--root", dir, "--cwd", dir})
	})
	var got struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, out)
	}
	if got.Kind != "codex" {
		t.Errorf("kind = %q, want \"codex\" — the legacy manifest was not read", got.Kind)
	}
}

// cmdApprove must print the same path ApproveKind stored, even when --root is
// a symlink.
func TestApproveKindPrintsTheCanonicalPath(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("filesystem does not support symlinks; nothing to test")
	}
	up := filepath.Join(t.TempDir(), "tandem.json")
	t.Setenv("HERDR_TANDEM_CONFIG", up)

	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks(real): %v", err)
	}

	out := captureStdout(t, func() int {
		return cmdApprove([]string{"--root", link, "--kind", "codex"})
	})
	var got struct {
		Approved string `json:"approved"`
		Repo     string `json:"repo"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Repo != resolvedReal {
		t.Fatalf("printed repo %q, want the resolved path %q", got.Repo, resolvedReal)
	}

	u, err := config.LoadUser(up)
	if err != nil {
		t.Fatal(err)
	}
	if !u.KindApproved(real, "codex") {
		t.Error("approved via the printed repo value, but KindApproved(real, ...) is false")
	}
}

func TestLaunchSpecNeverScopesTheBuilderNetwork(t *testing.T) {
	dir := withFixture(t, "{}", "")
	args := []string{"--role", "builder", "--kind", "codex", "--autonomy", "auto", "--root", dir, "--cwd", dir, "--dry-run"}
	t.Setenv("HERDR_JUDGE", "")
	off := captureStdout(t, func() int { return cmdLaunchSpec(args) })
	t.Setenv("HERDR_JUDGE", "1")
	t.Setenv("TYPESAFE_API_KEY", "k")
	on := captureStdout(t, func() int { return cmdLaunchSpec(args) })
	var plain, judged map[string]any
	if err := json.Unmarshal([]byte(off), &plain); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(on), &judged); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, judged) {
		t.Fatalf("judging changed the builder launch: off=%s on=%s", off, on)
	}
	if _, exists := judged["network_allow"]; exists {
		t.Fatalf("launch still records a network scope: %s", on)
	}
}

// End to end: a reviewer launch must never carry the builder's model.
func TestLaunchSpecReviewerCarriesNoBuilderModel(t *testing.T) {
	dir := withFixture(t,
		`{"builder":{"kind":"copilot","model":"claude-sonnet-5.5"},"reviewer":{"kind":"codex"}}`, "")
	if err := config.ApproveKind(config.UserConfigPath(), dir, "codex"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int {
		return cmdLaunchSpec([]string{"--role", "reviewer", "--root", dir, "--cwd", dir})
	})
	var got struct {
		Kind string   `json:"kind"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Kind != "codex" {
		t.Errorf("got kind %q, want codex", got.Kind)
	}
	for _, a := range got.Args {
		if a == "claude-sonnet-5.5" {
			t.Fatalf("reviewer launched with the builder's model: %v", got.Args)
		}
	}
}

func TestLaunchSpecReviewerWithoutBlockIsAManualGate(t *testing.T) {
	dir := withFixture(t, `{}`, "")
	if rc := cmdLaunchSpec([]string{"--role", "reviewer", "--root", dir, "--cwd", dir}); rc != 23 {
		t.Fatalf("got exit %d, want 23", rc)
	}
}

func TestLaunchSpecRefusesUnapprovedRepoReviewerKind(t *testing.T) {
	dir := withFixture(t, `{"reviewer":{"kind":"codex"}}`, "")
	if rc := cmdLaunchSpec([]string{"--role", "reviewer", "--root", dir, "--cwd", dir}); rc != 22 {
		t.Fatalf("got exit %d, want 22", rc)
	}
}

func TestConfigResolveReviewerNone(t *testing.T) {
	dir := withFixture(t, `{"builder":{"kind":"codex"}}`, "")
	out := captureStdout(t, func() int {
		return cmdConfig([]string{"resolve", "--role", "reviewer", "--root", dir})
	})
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Status != "none" {
		t.Fatalf("got status %q, want none: %s", got.Status, out)
	}
}

func TestConfigResolveReviewerHonoursOverrideFlags(t *testing.T) {
	dir := withFixture(t, `{"reviewer":{"kind":"codex","model":"block"}}`, "")
	out := captureStdout(t, func() int {
		return cmdConfig([]string{"resolve", "--role", "reviewer", "--root", dir, "--model", "flagged"})
	})
	var got struct {
		Status   string `json:"status"`
		Resolved struct {
			Model struct{ Value, From string } `json:"model"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Status != "resolved" || got.Resolved.Model.Value != "flagged" || got.Resolved.Model.From != "flag" {
		t.Fatalf("got %s", out)
	}
}
