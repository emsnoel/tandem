package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

// runCapture runs the CLI and returns its exit code, stdout and stderr.
func runCapture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we
	rc := run(args)
	wo.Close()
	we.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(ro)
	errb, _ := io.ReadAll(re)
	return rc, string(out), string(errb)
}

// oneJSON decodes stdout, requiring exactly one JSON object and nothing else.
func oneJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not a JSON object: %q: %v", out, err)
	}
	if dec.More() || strings.TrimSpace(out[dec.InputOffset():]) != "" {
		t.Fatalf("stdout holds more than one JSON value: %q", out)
	}
	return v
}

// snapshot records every file under dir with its size and content hash, so a
// command that must not write can be shown not to have.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, _ := os.ReadFile(p)
		sum := sha256.Sum256(raw)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return out
}

// sameSnapshot reports whether nothing but SQLite's own reader sidecars
// changed. A read-only connection to a WAL database may create an empty -wal
// and a -shm index next to it; those hold no data, are ignored by a later
// writer, and are the only difference allowed. The database, the registry and
// every other file must be byte-identical.
func sameSnapshot(a, b map[string]string) bool {
	emptyHash := sha256.Sum256(nil)
	empty := hex.EncodeToString(emptyHash[:])
	sidecar := func(name string) bool {
		return strings.HasSuffix(name, "state.sqlite-shm") || strings.HasSuffix(name, "state.sqlite-wal")
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	for k, v := range b {
		if _, ok := a[k]; ok {
			continue
		}
		if !sidecar(k) || (strings.HasSuffix(k, "-wal") && v != empty) {
			return false
		}
	}
	return true
}

func names(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// seedPausedRun creates the checkout's state database with one paused run, one
// attempt and one partial usage record, using plain SQL with foreign keys on.
func seedPausedRun(t *testing.T, root, home string) state.Identity {
	t.Helper()
	id, err := state.ResolveIdentity(root, home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(id.StateRoot, "state.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := func(c string) string { return strings.Repeat(c, 64) }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,1,'text/plain',1),(?,1,'text/plain',1)`, []any{h("a"), h("b")}},
		{`INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?,?,?,1)`, []any{id.CheckoutID, id.CanonicalRoot, id.GitCommonDir}},
		{`INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?,'.')`, []any{strings.Repeat("1", 32), id.CheckoutID}},
		{`INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,pause_code,created_ms) VALUES(?,?,?,?,2,'paused','stuck',1)`, []any{strings.Repeat("2", 32), strings.Repeat("1", 32), h("a"), h("b")}},
		{`INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?,'task-1',1,?,?,?)`, []any{strings.Repeat("2", 32), h("a"), h("a"), h("a")}},
		{`INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,version,is_current,created_ms) VALUES(?,?,'task-1',1,'stuck','pending','pending','available',1,1,1)`, []any{strings.Repeat("3", 32), strings.Repeat("2", 32)}},
		{`INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'builder','completed',?,?,'test')`, []any{strings.Repeat("4", 32), strings.Repeat("3", 32), h("a"), h("b")}},
		{`INSERT INTO usage_records(id,attempt_id,job_id,source_id,model,input_tokens,completeness,input_semantics) VALUES(?,?,?,'s1','m',10,'partial','unknown')`, []any{strings.Repeat("5", 32), strings.Repeat("3", 32), strings.Repeat("4", 32)}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %q: %v", q.sql, err)
		}
	}
	return id
}

func TestStateStatusJSONIsReadOnly(t *testing.T) {
	t.Run("fresh: nothing exists yet", func(t *testing.T) {
		root := pairedRepo(t)
		home := filepath.Join(t.TempDir(), "never-created")
		rc, out, errOut := runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
		if rc != exitcode.OK {
			t.Fatalf("exit %d, stderr %q", rc, errOut)
		}
		got := oneJSON(t, out)
		if got["initialized"] != false || got["engine"] != "durable" || got["current_run"] != nil || got["schema_version"] != nil {
			t.Fatalf("status %v", got)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("status created the state home: %v", err)
		}
		if errOut != "" {
			t.Fatalf("stderr %q", errOut)
		}
	})

	t.Run("paused run", func(t *testing.T) {
		root := pairedRepo(t)
		home := filepath.Join(t.TempDir(), "state-home")
		seedPausedRun(t, root, home)
		before := snapshot(t, home)
		rc, out, errOut := runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
		if rc != exitcode.OK {
			t.Fatalf("a paused run must exit 0, got %d (%q)", rc, errOut)
		}
		got := oneJSON(t, out)
		run, _ := got["current_run"].(map[string]any)
		usage, _ := got["usage"].(map[string]any)
		if got["initialized"] != true || got["engine"] != "durable" || got["readable"] != true ||
			run["state"] != "paused" || got["paused_reason"] != "stuck" || got["schema_version"] != float64(state.LatestSchema()) ||
			usage["completeness"] != "partial" || usage["records"] != float64(1) {
			t.Fatalf("status %v", got)
		}
		after := snapshot(t, home)
		if !sameSnapshot(before, after) {
			t.Fatalf("status wrote to the state home:\nbefore %v\nafter  %v", names(before), names(after))
		}
		if errOut != "" {
			t.Fatalf("stderr %q", errOut)
		}
	})

	t.Run("a newer schema is reported, not read", func(t *testing.T) {
		root := pairedRepo(t)
		home := filepath.Join(t.TempDir(), "state-home")
		id := seedPausedRun(t, root, home)
		db, _ := sql.Open("sqlite", filepath.Join(id.StateRoot, "state.sqlite"))
		if _, err := db.Exec(`PRAGMA user_version = 77`); err != nil {
			t.Fatal(err)
		}
		db.Close()
		before := snapshot(t, home)
		rc, out, _ := runCapture(t, "state", "status", "--json", "--root", root, "--state-home", home)
		if rc != exitcode.ManualGate {
			t.Fatalf("exit %d, want %d", rc, exitcode.ManualGate)
		}
		got := oneJSON(t, out)
		if got["readable"] != false || got["schema_version"] != float64(77) || got["supported_schema"] != float64(state.LatestSchema()) ||
			got["current_run"] != nil || !strings.Contains(got["diagnostic"].(string), "upgrade tandem") {
			t.Fatalf("status %v", got)
		}
		if !sameSnapshot(before, snapshot(t, home)) {
			t.Fatal("a refused status changed the state home")
		}
	})
}

func TestStateDispatchUsage(t *testing.T) {
	root := pairedRepo(t)
	for name, args := range map[string][]string{
		"no subcommand":      {"state"},
		"unknown subcommand": {"state", "bogus"},
		"unknown flag":       {"state", "status", "--json", "--nope"},
		"status needs json":  {"state", "status", "--root", root},
		"extra argument":     {"state", "status", "--json", "extra"},
	} {
		rc, out, errOut := runCapture(t, args...)
		if rc != exitcode.Usage {
			t.Errorf("%s: exit %d, want %d", name, rc, exitcode.Usage)
		}
		if out != "" {
			t.Errorf("%s: usage errors must not write stdout: %q", name, out)
		}
		if !strings.Contains(errOut, "tandem state") {
			t.Errorf("%s: stderr lacks the usage line: %q", name, errOut)
		}
	}
	found := false
	for _, v := range coreSubcommands {
		found = found || v == "state"
	}
	if !found {
		t.Error("coreSubcommands does not list state")
	}
}
