package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

// copilotHome seeds a Copilot home with the given config.json body and points
// COPILOT_HOME at it. An empty body writes no file at all, which is the
// "Copilot has never run here" case.
func copilotHome(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COPILOT_HOME", dir)
	return dir
}

// resolved is what the gate writes: Copilot stores the physical path, and on
// macOS t.TempDir() hands back a /var symlink to /private/var.
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

func copilotGate(t *testing.T) Gate {
	t.Helper()
	for _, g := range registry["copilot"] {
		if g.Name() == "project-trust" {
			return g
		}
	}
	t.Fatal("no copilot project-trust gate registered")
	return nil
}

// The real file opens with // comment lines, so a strict JSON parse fails on
// it. This is the case that would silently report every directory untrusted.
func TestCopilotTrustParsesJSONC(t *testing.T) {
	cwd := t.TempDir()
	copilotHome(t, `// User settings belong in settings.json.
// This file is managed automatically.
{
  "appTipShown": true,
  "trustedFolders": ["`+cwd+`"]
}`)
	a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{})
	if a.Status != agent.StatusCurrent {
		t.Fatalf("got %v (%s), want current", a.Status, a.Detail)
	}
}

func TestCopilotTrustReportsUntrustedWhenNotAuthorised(t *testing.T) {
	cwd := t.TempDir()
	copilotHome(t, `{"trustedFolders": ["/somewhere/else"]}`)
	a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{})
	if a.Status != agent.StatusManual {
		t.Fatalf("got %v, want manual", a.Status)
	}
	// A human who answers option 1 clears the dialog in front of them and
	// meets it again next pairing, so the remedy must name option 2.
	if !strings.Contains(a.Remedy, "remember this folder") {
		t.Errorf("remedy must name the remembering option; got %q", a.Remedy)
	}
}

func TestCopilotTrustMissingConfigIsManualWhenNotAuthorised(t *testing.T) {
	cwd := t.TempDir()
	copilotHome(t, "")
	a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{})
	if a.Status != agent.StatusManual {
		t.Fatalf("got %v, want manual", a.Status)
	}
}

// Unauthorised, the gate must not touch the file: a repository asking for a
// copilot builder must never be what trusts the folder it ships in.
func TestCopilotTrustNeverWritesWhenNotAuthorised(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `{"trustedFolders": []}`)
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{})
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if a.Disposition != agent.Report {
		t.Errorf("got disposition %v, want report", a.Disposition)
	}
	if string(before) != string(after) {
		t.Errorf("gate rewrote %s", filepath.Join(dir, "config.json"))
	}
}

// --- Apply path -----------------------------------------------------------

func trustedFolders(t *testing.T, dir string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, folders, err := parseCopilotConfig(body)
	if err != nil {
		t.Fatalf("re-parsing what we wrote: %v", err)
	}
	return folders
}

func TestCopilotTrustAppliesWhenAuthorised(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `{"trustedFolders": []}`)
	a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{Authorised: true})
	if a.Status != agent.StatusApplied {
		t.Fatalf("got %v (%s), want applied", a.Status, a.Detail)
	}
	if a.Disposition != agent.Apply {
		t.Errorf("got disposition %v, want apply", a.Disposition)
	}
	if got := trustedFolders(t, dir); len(got) != 1 || got[0] != resolved(t, cwd) {
		t.Fatalf("got %v, want [%s]", got, resolved(t, cwd))
	}
	// A second run is a no-op, not a duplicate entry.
	if a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{Authorised: true}); a.Status != agent.StatusCurrent {
		t.Errorf("second run: got %v, want current", a.Status)
	}
	if got := trustedFolders(t, dir); len(got) != 1 {
		t.Errorf("second run duplicated the entry: %v", got)
	}
}

// The banner cannot be emitted by encoding/json, and the other keys are values
// tandem knows nothing about. Both must survive the write untouched.
func TestCopilotTrustPreservesBannerAndOtherKeys(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `// User settings belong in settings.json.
// This file is managed automatically.
{
  "firstLaunchAt": "2026-07-28T21:33:09.375Z",
  "appTipShown": true,
  "someNumber": 12345678901234567890,
  "trustedFolders": []
}`)
	if a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{Authorised: true}); a.Status != agent.StatusApplied {
		t.Fatalf("got %v (%s), want applied", a.Status, a.Detail)
	}
	body, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"// User settings belong in settings.json.",
		"// This file is managed automatically.",
		`"firstLaunchAt": "2026-07-28T21:33:09.375Z"`,
		`"appTipShown": true`,
		// Routed through `any` this would come back as 1.2345678901234567e+19.
		`"someNumber": 12345678901234567890`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("write lost %s\n--- got ---\n%s", want, got)
		}
	}
}

func TestCopilotTrustDryRunWritesNothing(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `{"trustedFolders": []}`)
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	a := copilotGate(t).Check(cwd, agent.RoleBuilder, Options{Authorised: true, DryRun: true})
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if a.Status != agent.StatusApplied {
		t.Errorf("got %v, want applied (a dry run reports what it would do)", a.Status)
	}
	if string(before) != string(after) {
		t.Errorf("dry run wrote to %s", filepath.Join(dir, "config.json"))
	}
}

func TestCopilotTrustRecordsWhatItAdded(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `{"trustedFolders": []}`)
	copilotGate(t).Check(cwd, agent.RoleBuilder, Options{Authorised: true})
	if got := readLedger(filepath.Join(dir, ".tandem-trusted")); len(got) != 1 || got[0] != resolved(t, cwd) {
		t.Fatalf("got ledger %v, want [%s]", got, resolved(t, cwd))
	}
}

// --- Clean ------------------------------------------------------------------

func TestCleanRemovesWhatTandemAdded(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `{"trustedFolders": []}`)
	copilotGate(t).Check(cwd, agent.RoleBuilder, Options{Authorised: true})

	removed, err := Clean("copilot", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("got removed=false, want true")
	}
	if got := trustedFolders(t, dir); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
	if got := readLedger(filepath.Join(dir, ".tandem-trusted")); len(got) != 0 {
		t.Errorf("ledger still lists %v", got)
	}
}

// A folder the human trusted by hand is absent from the ledger, so a teardown
// must leave it exactly where it is.
func TestCleanLeavesAHumansOwnEntryAlone(t *testing.T) {
	cwd := t.TempDir()
	dir := copilotHome(t, `{"trustedFolders": ["`+cwd+`"]}`)
	removed, err := Clean("copilot", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("got removed=true; an unledgered entry is a human's and is never touched")
	}
	if got := trustedFolders(t, dir); len(got) != 1 || got[0] != cwd {
		t.Fatalf("got %v, want the human's entry intact", got)
	}
}
