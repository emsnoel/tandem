package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
)

type durable struct {
	root, home, relay, dir string
	run, project           string
	attempt, job, effect   string
	nonce                  string
}

// durableRepo is a git checkout with a durable run: marker, manifest and the
// state rows an attempt waiting on its builder dispatch needs.
func durableRepo(t *testing.T) *durable {
	t.Helper()
	root, _ := buildRepo(t, "", 0)
	os.RemoveAll(filepath.Join(root, ".tandem-relay", "build")) // a durable run has no legacy build
	d := &durable{root: root, home: filepath.Join(t.TempDir(), "state-home"), relay: paths.Relay(root),
		run: strings.Repeat("a", 32), project: strings.Repeat("b", 32), attempt: strings.Repeat("c", 32),
		job: strings.Repeat("d", 32), effect: strings.Repeat("e", 32), nonce: strings.Repeat("f", 32)}
	dir, err := controller.InitRunDir(d.relay, controller.ActiveRun{RunID: d.run, ProjectID: d.project},
		controller.RunManifest{RunID: d.run, Tasks: []controller.ManifestTask{{Ordinal: 1, ID: "receipt-store"}, {Ordinal: 2, ID: "second-task"}}})
	if err != nil {
		t.Fatal(err)
	}
	d.dir = dir
	// Ingest observes the builder through the run's transport; this one stands
	// in for Herdr for the length of the test.
	old := durableTransport
	durableTransport = func(transportSpec) (runner.Transport, error) { return &cliTransport{}, nil }
	t.Cleanup(func() { durableTransport = old })

	id, err := state.ResolveIdentity(root, d.home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	art, _ := artifact.Open(id.StateRoot)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(id.StateRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	put := func(s string) string {
		h, _ := art.Put(context.Background(), strings.NewReader(s))
		db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,'text/plain',1)`, h, len(s))
		return h
	}
	// A run names its builder: ingest builds the run's transport from it.
	plan, cfg, brief, packet, start, ev := put("plan"), put(`{"root":"`+root+`","builder_kind":"codex","builder_session":"builder","planner_session":"planner"}`),
		put("brief"), put("packet"), put("start"), put("dispatch event")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?,?,?,1)`, []any{id.CheckoutID, id.CanonicalRoot, id.GitCommonDir}},
		{`INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?,'.')`, []any{d.project, id.CheckoutID}},
		{`INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,created_ms) VALUES(?,?,?,?,2,'running',1)`, []any{d.run, d.project, plan, cfg}},
		{`INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?,'receipt-store',1,?,?,?)`, []any{d.run, brief, brief, brief}},
		{`INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,start_snapshot_hash,packet_hash,version,is_current,created_ms)
			VALUES(?,?,'receipt-store',1,'pending','pending','pending','available',?,?,1,1,1)`, []any{d.attempt, d.run, start, packet}},
		{`INSERT INTO events(id,run_id,attempt_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?,?,'attempt_created',?,0,1,1)`, []any{strings.Repeat("9", 32), d.run, d.attempt, ev}},
		{`INSERT INTO effects(id,run_id,attempt_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms) VALUES(?,?,?,?,'builder_dispatch',0,?,?,'delivering',1,1,1)`,
			[]any{d.effect, d.run, d.attempt, strings.Repeat("9", 32), packet, d.nonce}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %q: %v", q.sql, err)
		}
	}
	return d
}

func (d *durable) newEvent(t *testing.T, kind string, extra ...string) (int, map[string]any, string) {
	t.Helper()
	// --task 1 first, so an extra --task overrides it.
	args := append([]string{"build", "event", "new", "--root", d.root, "--kind", kind, "--task", "1", "--attempt", d.attempt, "--job", d.job,
		"--effect", d.effect, "--nonce", d.nonce}, extra...)
	rc, out, errOut := runCapture(t, args...)
	var got map[string]any
	if rc == exitcode.OK {
		got = oneJSON(t, out)
	}
	return rc, got, errOut
}

func TestBuildEventNewWritesAVersionedEnvelopeAndImmutablePayload(t *testing.T) {
	d := durableRepo(t)
	report := filepath.Join(t.TempDir(), "report.md")
	os.WriteFile(report, []byte("the builder's report\n"), 0o644)

	rc, got, errOut := d.newEvent(t, "builder_submitted", "--task", "2", "--payload", report)
	if rc != exitcode.OK {
		t.Fatalf("exit %d: %s", rc, errOut)
	}
	queue := got["queue_file"].(string)
	if filepath.Dir(queue) != filepath.Join(d.dir, "q") || !strings.HasSuffix(queue, ".json") {
		t.Fatalf("queue file %s", queue)
	}
	raw, err := os.ReadFile(queue)
	if err != nil {
		t.Fatal(err)
	}
	env, err := controller.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("the helper wrote an envelope the controller refuses: %v\n%s", err, raw)
	}
	if env.TaskID != "second-task" || env.RunID != d.run || env.AttemptID != d.attempt || env.Nonce != d.nonce || env.EventID != got["event_id"] {
		t.Fatalf("envelope %+v", env)
	}
	body, err := os.ReadFile(filepath.Join(d.dir, env.PayloadPath))
	if err != nil || string(body) != "the builder's report\n" {
		t.Fatalf("payload %q %v", body, err)
	}
	if !strings.HasPrefix(env.PayloadPath, "payloads/") {
		t.Fatalf("payload path %s", env.PayloadPath)
	}
	_, second, _ := d.newEvent(t, "builder_started")
	if second["event_id"] == got["event_id"] {
		t.Fatal("event ids are not random")
	}
}

func TestBuildEventNewRefusesWhatABuilderMustNotSend(t *testing.T) {
	d := durableRepo(t)
	for _, kind := range []string{"accept_requested", "check_completed", "waive_requested", "budget_changed", "stop_requested", "made_up"} {
		if rc, _, _ := d.newEvent(t, kind); rc != exitcode.Usage {
			t.Errorf("%s: exit %d, want usage", kind, rc)
		}
	}
	if rc, _, _ := d.newEvent(t, "builder_started", "--task", "9"); rc == exitcode.OK {
		t.Error("a task the run does not have")
	}
	big := filepath.Join(t.TempDir(), "big")
	os.WriteFile(big, make([]byte, controller.MaxPayloadBytes+1), 0o644)
	if rc, _, _ := d.newEvent(t, "builder_submitted", "--payload", big); rc == exitcode.OK {
		t.Error("an oversized payload")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(big, link)
	if rc, _, _ := d.newEvent(t, "builder_submitted", "--payload", link); rc == exitcode.OK {
		t.Error("a symlinked payload")
	}
	if rc, _, _ := d.newEvent(t, "builder_started", "--nonce", "short"); rc == exitcode.OK {
		t.Error("a malformed nonce")
	}
	if left, _ := filepath.Glob(filepath.Join(d.dir, "q", "*")); len(left) != 0 {
		t.Errorf("a refused call queued %v", left)
	}
	// Without a durable run there is nothing to write to.
	plain, _ := buildRepo(t, "", 0)
	rc, _, _ := runCapture(t, "build", "event", "new", "--root", plain, "--kind", "builder_started", "--task", "1", "--attempt", d.attempt, "--job", d.job, "--effect", d.effect, "--nonce", d.nonce)
	if rc == exitcode.OK {
		t.Error("an event for a relay with no durable run")
	}
}

func (d *durable) ingest(t *testing.T, claimed string, extra ...string) (int, map[string]any, string) {
	t.Helper()
	args := append([]string{"build", "event", "ingest", "--root", d.root, "--state-home", d.home, "--claimed", claimed}, extra...)
	rc, out, errOut := runCapture(t, args...)
	var got map[string]any
	if strings.TrimSpace(out) != "" {
		got = oneJSON(t, out)
	}
	return rc, got, errOut
}

func claim(t *testing.T, queue string) string {
	t.Helper()
	taken := queue + ".taken"
	if err := os.Rename(queue, taken); err != nil {
		t.Fatal(err)
	}
	return taken
}

func TestBuildEventIngestCommitsReplaysAndQuarantines(t *testing.T) {
	d := durableRepo(t)
	_, got, _ := d.newEvent(t, "builder_started")
	taken := claim(t, got["queue_file"].(string))

	rc, res, errOut := d.ingest(t, taken)
	if rc != exitcode.OK || res["disposition"] != "committed" || res["version"] != float64(2) {
		t.Fatalf("exit %d %v %s", rc, res, errOut)
	}
	if _, err := os.Stat(taken); !os.IsNotExist(err) {
		t.Fatal("the claimed file was not removed after the commit")
	}
	if _, err := os.Stat(filepath.Join(d.dir, "receipts", got["event_id"].(string)+".json")); err != nil {
		t.Fatalf("no durable receipt: %v", err)
	}

	// A replay of the same message, as after a bridge crash, is a duplicate.
	again := filepath.Join(d.dir, "q", got["event_id"].(string)+".json.taken")
	envelope := rebuildEnvelope(t, d, got["event_id"].(string))
	os.WriteFile(again, envelope, 0o644)
	if rc, res, _ := d.ingest(t, again); rc != exitcode.OK || res["disposition"] != "duplicate" || res["version"] != float64(2) {
		t.Fatalf("replay: exit %d %v", rc, res)
	}

	// A message that can never be applied is quarantined and the call still succeeds.
	forged := filepath.Join(d.dir, "q", "forged.json.taken")
	os.WriteFile(forged, []byte(`{"schema_version":2,"kind":"accept_requested"}`), 0o644)
	rc, res, _ = d.ingest(t, forged)
	if rc != exitcode.OK || res["disposition"] != "quarantined" || res["reason"] == "" {
		t.Fatalf("forged: exit %d %v", rc, res)
	}
	if _, err := os.Stat(filepath.Join(d.dir, "quarantine", "forged.json.taken.reason")); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rebuildEnvelope recreates the envelope for an already-ingested event, as a
// replayed claimed file would hold it.
func rebuildEnvelope(t *testing.T, d *durable, eventID string) []byte {
	t.Helper()
	payloads, _ := filepath.Glob(filepath.Join(d.dir, "payloads", "*"))
	if len(payloads) != 1 {
		t.Fatalf("payloads %v", payloads)
	}
	body := mustRead(t, payloads[0])
	env := controller.Envelope{SchemaVersion: 2, EventID: eventID, RunID: d.run, TaskID: "receipt-store", AttemptID: d.attempt,
		JobID: d.job, EffectID: d.effect, Nonce: d.nonce, Kind: "builder_started",
		PayloadPath: "payloads/" + filepath.Base(payloads[0]), PayloadHash: sumHex(body)}
	raw, _ := json.Marshal(env)
	return raw
}

func TestBuildEventIngestWaitsForASettledBuilderBeforeASubmission(t *testing.T) {
	d := durableRepo(t)
	_, got, _ := d.newEvent(t, "builder_submitted")
	taken := claim(t, got["queue_file"].(string))
	rc, res, errOut := d.ingest(t, taken)
	if rc == exitcode.OK || res != nil || !strings.Contains(errOut, "settled") {
		t.Fatalf("exit %d %v %q: an unsettled submission must be retried, not quarantined", rc, res, errOut)
	}
	if _, err := os.Stat(taken); err != nil {
		t.Fatal("the claimed file must stay for a retry")
	}
	if entries, _ := os.ReadDir(filepath.Join(d.dir, "quarantine")); len(entries) != 0 {
		t.Fatalf("quarantined: %v", entries)
	}
}

func TestBuildEventUsage(t *testing.T) {
	d := durableRepo(t)
	for name, args := range map[string][]string{
		"no subcommand":      {"build", "event"},
		"unknown subcommand": {"build", "event", "bogus"},
		"new unknown flag":   {"build", "event", "new", "--root", d.root, "--nope"},
		"new missing kind":   {"build", "event", "new", "--root", d.root},
		"ingest no file":     {"build", "event", "ingest", "--root", d.root},
		"ingest extra":       {"build", "event", "ingest", "--root", d.root, "--claimed", "/x", "extra"},
	} {
		if rc, out, _ := runCapture(t, args...); rc != exitcode.Usage || out != "" {
			t.Errorf("%s: exit %d stdout %q", name, rc, out)
		}
	}
}

func sumHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
