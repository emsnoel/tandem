package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/paths"
)

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkhome mirrors the synthetic CODEX_HOME from tests/test-hook-trust.sh: one
// user hook file and one enabled plugin hook file.
func mkhome(t *testing.T, pluginTrusted bool) string {
	t.Helper()
	h := t.TempDir()
	user := filepath.Join(h, "hooks.json")
	plugin := filepath.Join(h, "plugins", "cache", "mkt", "plug", "1.0.0", "hooks", "hooks.json")
	writeFixture(t, user, `{"hooks":{"SessionStart":[]}}`+"\n")
	writeFixture(t, plugin, `{"hooks":{"Stop":[]}}`+"\n")
	cfg := `[plugins."plug@mkt"]` + "\nenabled = true\n\n" +
		`[hooks.state."` + user + `:session_start:0:0"]` + "\n" +
		`trusted_hash = "sha256:aaa"` + "\n\n"
	if pluginTrusted {
		cfg += `[hooks.state."plug@mkt:hooks/hooks.json:stop:0:0"]` + "\n" +
			`trusted_hash = "sha256:bbb"` + "\n"
	}
	writeFixture(t, filepath.Join(h, "config.toml"), cfg)
	return h
}

func finding(t *testing.T, findings []Finding) Finding {
	t.Helper()
	if len(findings) != 1 {
		t.Fatalf("want one finding, got %+v", findings)
	}
	return findings[0]
}

func TestHookClassificationPortPreservesTwentyAssertions(t *testing.T) {
	t.Run("finds user and enabled plugin sources", func(t *testing.T) {
		if got := len(Sources(mkhome(t, true))); got != 2 {
			t.Fatalf("want 2 sources, got %d", got)
		}
	})
	t.Run("derives plugin marketplace trust key", func(t *testing.T) {
		var found bool
		for _, s := range Sources(mkhome(t, true)) {
			found = found || s.Key == "plug@mkt"
		}
		if !found {
			t.Fatal("plug@mkt source not found")
		}
	})
	t.Run("ignores marketplace staging", func(t *testing.T) {
		h := mkhome(t, true)
		writeFixture(t, filepath.Join(h, ".tmp", "marketplaces", "x", "plugins", "y", "hooks", "hooks.json"), "{}\n")
		if got := len(Sources(h)); got != 2 {
			t.Fatalf("want 2 sources after staging file, got %d", got)
		}
	})
	t.Run("reports one unreviewed source", func(t *testing.T) {
		finding(t, Classify(mkhome(t, false)))
	})
	t.Run("names unreviewed kind", func(t *testing.T) {
		if got := finding(t, Classify(mkhome(t, false))).Kind; got != "unreviewed" {
			t.Fatalf("want unreviewed, got %q", got)
		}
	})
	t.Run("names unreviewed path", func(t *testing.T) {
		h := mkhome(t, false)
		if got := finding(t, Classify(h)).Path; !strings.HasSuffix(got, "plug/1.0.0/hooks/hooks.json") {
			t.Fatalf("unexpected path %q", got)
		}
	})
	t.Run("all accepted sources are silent", func(t *testing.T) {
		if got := Classify(mkhome(t, true)); len(got) != 0 {
			t.Fatalf("want silence, got %+v", got)
		}
	})
	t.Run("classification is independent of strict policy", func(t *testing.T) {
		t.Setenv("HERDR_STRICT_HOOK_CHECK", "1")
		if got := finding(t, Classify(mkhome(t, false))).Kind; got != "unreviewed" {
			t.Fatalf("want classifier finding, got %q", got)
		}
	})
	t.Run("classification is independent of skip policy", func(t *testing.T) {
		t.Setenv("HERDR_SKIP_HOOK_CHECK", "1")
		if got := finding(t, Classify(mkhome(t, false))).Kind; got != "unreviewed" {
			t.Fatalf("want classifier finding, got %q", got)
		}
	})
	t.Run("missing config fails open", func(t *testing.T) {
		if got := Classify(t.TempDir()); got != nil {
			t.Fatalf("want nil, got %+v", got)
		}
	})
	t.Run("garbage config fails open", func(t *testing.T) {
		h := mkhome(t, true)
		writeFixture(t, filepath.Join(h, "config.toml"), "\x00not toml")
		if got := Classify(h); len(got) != 0 {
			t.Fatalf("want silence, got %+v", got)
		}
	})
	t.Run("clean run writes baseline", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		if _, err := os.Stat(filepath.Join(h, paths.HookBaselineName)); err != nil {
			t.Fatalf("baseline not written: %v", err)
		}
	})
	t.Run("baseline records both sources", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		b, err := os.ReadFile(filepath.Join(h, paths.HookBaselineName))
		if err != nil || strings.Count(strings.TrimSpace(string(b)), "\n")+1 != 2 {
			t.Fatalf("want two baseline rows, got %q (%v)", b, err)
		}
	})
	// A machine that paired before the rename holds a .herdr-hook-baseline. It
	// must keep being the baseline: writing a second one under the new name
	// would compare today's hooks against a file recorded moments ago and
	// report "unchanged" for an edit made last week.
	t.Run("an existing legacy baseline is used, not duplicated", func(t *testing.T) {
		h := mkhome(t, true)
		legacy := filepath.Join(h, ".herdr-hook-baseline")
		writeFixture(t, legacy, "stale\n")
		Classify(h)
		b, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatalf("legacy baseline gone: %v", err)
		}
		if strings.Contains(string(b), "stale") {
			t.Error("legacy baseline was not refreshed in place")
		}
		if _, err := os.Stat(filepath.Join(h, paths.HookBaselineName)); err == nil {
			t.Error("a second baseline was written under the new name; there must be exactly one")
		}
	})
	t.Run("unchanged hooks stay quiet", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		if got := Classify(h); len(got) != 0 {
			t.Fatalf("want silence, got %+v", got)
		}
	})
	t.Run("detects changed hook", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		writeFixture(t, filepath.Join(h, "plugins", "cache", "mkt", "plug", "1.0.0", "hooks", "hooks.json"), `{"changed":true}`)
		finding(t, Classify(h))
	})
	t.Run("names changed kind", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		writeFixture(t, filepath.Join(h, "hooks.json"), `{"changed":true}`)
		if got := finding(t, Classify(h)).Kind; got != "changed" {
			t.Fatalf("want changed, got %q", got)
		}
	})
	t.Run("names changed path", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		p := filepath.Join(h, "hooks.json")
		writeFixture(t, p, `{"changed":true}`)
		if got := finding(t, Classify(h)).Path; got != p {
			t.Fatalf("want %q, got %q", p, got)
		}
	})
	t.Run("finding does not refresh baseline", func(t *testing.T) {
		h := mkhome(t, true)
		Classify(h)
		base := filepath.Join(h, paths.HookBaselineName)
		before, err := os.ReadFile(base)
		if err != nil {
			t.Fatalf("no baseline to compare against: %v", err)
		}
		writeFixture(t, filepath.Join(h, "hooks.json"), `{"changed":true}`)
		Classify(h)
		after, _ := os.ReadFile(base)
		if string(after) != string(before) {
			t.Fatal("baseline changed while reporting drift")
		}
	})
	t.Run("no sources writes no baseline", func(t *testing.T) {
		h := t.TempDir()
		writeFixture(t, filepath.Join(h, "config.toml"), "\n")
		Classify(h)
		for _, n := range []string{paths.HookBaselineName, ".herdr-hook-baseline"} {
			if _, err := os.Stat(filepath.Join(h, n)); !os.IsNotExist(err) {
				t.Fatalf("unexpected baseline %s: %v", n, err)
			}
		}
	})
	t.Run("explicit home does not touch real codex home", func(t *testing.T) {
		real := t.TempDir()
		t.Setenv("HOME", real)
		Classify(mkhome(t, true))
		for _, n := range []string{paths.HookBaselineName, ".herdr-hook-baseline"} {
			if _, err := os.Stat(filepath.Join(real, ".codex", n)); !os.IsNotExist(err) {
				t.Fatalf("real home was touched (%s): %v", n, err)
			}
		}
	})
}
