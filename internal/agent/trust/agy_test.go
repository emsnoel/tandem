package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
)

// agySettings points HOME at a temp dir and writes agy's settings.json. An
// empty body writes no file.
func agySettings(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func agyDoc(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func agyTrusted(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal(agyDoc(t, dir)["trustedWorkspaces"], &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAgyTrustReportsUntrustedWhenNotAuthorised(t *testing.T) {
	cwd := t.TempDir()
	agySettings(t, `{"trustedWorkspaces":["/other"]}`)
	a := agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{})
	if a.Status != agent.StatusManual || a.Disposition != agent.Report {
		t.Fatalf("got %v/%v, want manual/report", a.Status, a.Disposition)
	}
	if !strings.Contains(a.Remedy, "Yes, I trust this folder") {
		t.Fatalf("remedy lacks the dialog answer: %q", a.Remedy)
	}
}

func TestAgyTrustMissingSettingsIsManualWhenNotAuthorised(t *testing.T) {
	cwd := t.TempDir()
	agySettings(t, "")
	a := agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{})
	if a.Status != agent.StatusManual {
		t.Fatalf("got %v, want manual", a.Status)
	}
}

func TestAgyTrustNeverWritesWhenNotAuthorised(t *testing.T) {
	cwd := t.TempDir()
	dir := agySettings(t, "")
	agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{})
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		t.Fatal("settings.json was written without authorisation")
	}
}

func TestAgyTrustAppliesWhenAuthorised(t *testing.T) {
	cwd := t.TempDir()
	dir := agySettings(t, "")
	a := agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{Authorised: true})
	if a.Status != agent.StatusApplied {
		t.Fatalf("got %v (%s), want applied", a.Status, a.Detail)
	}
	got := agyTrusted(t, dir)
	if len(got) != 1 || got[0] != resolved(t, cwd) {
		t.Fatalf("trustedWorkspaces = %v, want [%s]", got, resolved(t, cwd))
	}
}

func TestAgyTrustAcceptsUnresolvedPath(t *testing.T) {
	cwd := t.TempDir()
	agySettings(t, `{"trustedWorkspaces":["`+cwd+`"]}`)
	a := agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{})
	if a.Status != agent.StatusCurrent {
		t.Fatalf("got %v, want current", a.Status)
	}
}

func TestAgyTrustPreservesOtherKeys(t *testing.T) {
	cwd := t.TempDir()
	dir := agySettings(t, `{"model":"Gemini 4 Neon (Low)","x":{"nested":[1,2]},"trustedWorkspaces":["/a"]}`)
	a := agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{Authorised: true})
	if a.Status != agent.StatusApplied {
		t.Fatalf("got %v (%s), want applied", a.Status, a.Detail)
	}
	doc := agyDoc(t, dir)
	if string(doc["model"]) != `"Gemini 4 Neon (Low)"` || string(doc["x"]) != `{"nested":[1,2]}` {
		t.Fatalf("other keys changed: model=%s x=%s", doc["model"], doc["x"])
	}
	got := agyTrusted(t, dir)
	if len(got) != 2 || got[0] != "/a" {
		t.Fatalf("trustedWorkspaces = %v, want /a first", got)
	}
}

func TestAgyTrustDryRunWritesNothing(t *testing.T) {
	cwd := t.TempDir()
	dir := agySettings(t, "")
	a := agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{Authorised: true, DryRun: true})
	if a.Status != agent.StatusApplied {
		t.Fatalf("got %v, want applied", a.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		t.Fatal("dry run wrote settings.json")
	}
	if _, err := os.Stat(filepath.Join(dir, ".tandem-trusted")); err == nil {
		t.Fatal("dry run wrote the ledger")
	}
}

func TestAgyTrustRecordsWhatItAdded(t *testing.T) {
	cwd := t.TempDir()
	dir := agySettings(t, "")
	agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{Authorised: true})
	got := readLedger(filepath.Join(dir, ".tandem-trusted"))
	if len(got) != 1 || got[0] != resolved(t, cwd) {
		t.Fatalf("ledger = %v, want [%s]", got, resolved(t, cwd))
	}
}

func TestCleanAgyRemovesWhatTandemAdded(t *testing.T) {
	cwd := t.TempDir()
	dir := agySettings(t, `{"trustedWorkspaces":["/a"]}`)
	agyProjectTrust{}.Check(cwd, agent.RoleBuilder, Options{Authorised: true})
	removed, err := Clean("agy", cwd)
	if err != nil || !removed {
		t.Fatalf("Clean = %v, %v; want true, nil", removed, err)
	}
	got := agyTrusted(t, dir)
	if len(got) != 1 || got[0] != "/a" {
		t.Fatalf("trustedWorkspaces = %v, want [/a]", got)
	}
	if l := readLedger(filepath.Join(dir, ".tandem-trusted")); len(l) != 0 {
		t.Fatalf("ledger not emptied: %v", l)
	}
}

func TestCleanAgyLeavesAHumansOwnEntryAlone(t *testing.T) {
	cwd := t.TempDir()
	body := `{"trustedWorkspaces":["` + resolved(t, cwd) + `"]}`
	dir := agySettings(t, body)
	removed, err := Clean("agy", cwd)
	if err != nil || removed {
		t.Fatalf("Clean = %v, %v; want false, nil", removed, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(after) != body {
		t.Fatalf("file changed: %s", after)
	}
}
