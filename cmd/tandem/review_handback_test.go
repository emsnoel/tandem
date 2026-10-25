package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

const handbackPolicy = `{
 "schema_version": 1,
 "stages": [
  {"stage": "builder_review", "role": "reviewer", "provider": "codex", "model": "gpt-5.4-codex", "distinct_from_writer": false},
  {"stage": "independent_verification", "role": "planner", "provider": "claude", "model": "claude-opus-5-5", "distinct_from_writer": true}
 ],
 "checks": {"required": true},
 "evidence": {"task_brief": true, "constraints": true, "dependency_facts": true, "check_results": true, "patch_and_tree": true}
}`

func TestReviewHandbackRequiresIdentity(t *testing.T) {
	if err := validateReviewDelivery("mailbox", "", "policy"); err == nil {
		t.Fatal("mailbox accepted without collaboration")
	}
	if err := validateReviewDelivery("mailbox", "collaboration", "policy"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewHandbackDeliveryModes(t *testing.T) {
	for name, tc := range map[string]struct {
		delivery, collab, policy string
		ok                       bool
	}{
		"the default":                 {"", "", "", true},
		"manual":                      {"manual", "", "", true},
		"manual in a collaboration":   {"manual", "c", "", true},
		"manual with a policy":        {"manual", "c", "p", false},
		"a policy and no delivery":    {"", "c", "p", false},
		"mailbox without a policy":    {"mailbox", "c", "", false},
		"mailbox with nothing":        {"mailbox", "", "", false},
		"mailbox without a collab":    {"mailbox", "", "p", false},
		"an unknown delivery":         {"carrier-pigeon", "c", "p", false},
		"a differently cased mailbox": {"Mailbox", "c", "p", false},
	} {
		if err := validateReviewDelivery(tc.delivery, tc.collab, tc.policy); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func (e *cliEnv) runCount() int {
	e.t.Helper()
	if _, bound, _ := state.LookupIdentity(e.root, e.home); !bound {
		return 0
	}
	var n int
	if err := e.stateDB().QueryRow(`SELECT count(*) FROM runs`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// A refused start leaves no run and no state behind, and a run keeps the
// protocol it was started with.
func TestReviewHandbackStartIsOptInAndStoresItsProtocol(t *testing.T) {
	policy := newCLI(t).file("policy.json", handbackPolicy)
	for name, args := range map[string][]string{
		"mailbox alone":            {"--review-delivery", "mailbox"},
		"a policy alone":           {"--acceptance-policy", policy},
		"an unknown delivery":      {"--review-delivery", "carrier-pigeon"},
		"an unreadable policy":     {"--review-delivery", "mailbox", "--acceptance-policy", filepath.Join(t.TempDir(), "absent.json")},
		"a policy with a weakness": {"--review-delivery", "mailbox", "--acceptance-policy", "WEAK"},
	} {
		e := newCLI(t)
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		id := e.createCollaboration()
		args = append([]string{"--collaboration", id}, args...)
		for i, a := range args {
			if a == "WEAK" {
				args[i] = e.file("weak.json", strings.Replace(handbackPolicy, `"distinct_from_writer": true`, `"distinct_from_writer": false`, 1))
			}
		}
		if rc, _, _ := e.start(args...); rc == exitcode.OK {
			t.Errorf("%s: the start was accepted", name)
		}
		if n := e.runCount(); n != 0 {
			t.Errorf("%s: a refused start left %d runs", name, n)
		}
		if _, err := os.Stat(filepath.Join(e.root, ".tandem-relay", "active-run.json")); err == nil {
			t.Errorf("%s: a refused start left an active-run marker", name)
		}
	}

	protocols := map[string]int{}
	for name, mailbox := range map[string]bool{"default": false, "mailbox": true} {
		e := newCLI(t)
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		args := []string{}
		if mailbox {
			args = []string{"--collaboration", e.createCollaboration(), "--review-delivery", "mailbox", "--acceptance-policy", e.file("policy.json", handbackPolicy)}
		}
		rc, got, errOut := e.start(args...)
		if rc != exitcode.OK {
			t.Fatalf("%s: %d %s", name, rc, errOut)
		}
		runID := got["run_id"].(string)
		man, err := controller.ReadManifest(controller.RunDir(filepath.Join(e.root, ".tandem-relay"), runID))
		if err != nil {
			t.Fatal(err)
		}
		id, _, _ := state.LookupIdentity(e.root, e.home)
		st, err := state.Open(context.Background(), id.StateRoot)
		if err != nil {
			t.Fatal(err)
		}
		art, _ := artifact.Open(id.StateRoot)
		// Whatever the environment says later, the run keeps its protocol.
		t.Setenv("TANDEM_BUILD_ENGINE", "")
		protocols[name], err = controller.RunReviewProtocol(context.Background(), st, art, runID)
		st.Close()
		if err != nil {
			t.Fatal(err)
		}
		if (man.ReviewProtocol == 3) != mailbox {
			t.Errorf("%s: manifest protocol %d", name, man.ReviewProtocol)
		}
	}
	if protocols["default"] != 2 || protocols["mailbox"] != 3 {
		t.Errorf("stored protocols %v", protocols)
	}
}

func (d *durable) setProtocol(t *testing.T, protocol int) {
	t.Helper()
	m, err := controller.ReadManifest(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	m.ReviewProtocol = protocol
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(d.dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (d *durable) envelopeOf(t *testing.T, got map[string]any) controller.Envelope {
	t.Helper()
	raw, err := os.ReadFile(got["queue_file"].(string))
	if err != nil {
		t.Fatal(err)
	}
	env, err := controller.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("the helper wrote an envelope the controller refuses: %v\n%s", err, raw)
	}
	return env
}

// The message version follows the run's stored protocol: a reviewer speaks 3
// to a run that opted in, and a run that did not never receives a 3.
func TestReviewHandbackEventVersionFollowsTheStoredProtocol(t *testing.T) {
	result := filepath.Join(t.TempDir(), "result.json")
	os.WriteFile(result, []byte("{\"schema_version\":1,\"status\":\"passed\",\"findings\":[]}\n"), 0o644)

	old := durableRepo(t)
	if rc, _, errOut := old.newEvent(t, "review_started"); rc != exitcode.Usage || !strings.Contains(errOut, "review_started") {
		t.Fatalf("review_started on a run that did not opt in: %d %s", rc, errOut)
	}
	rc, got, errOut := old.newEvent(t, "review_result", "--payload", result)
	if rc != exitcode.OK {
		t.Fatalf("%d %s", rc, errOut)
	}
	if v := old.envelopeOf(t, got).SchemaVersion; v != 2 {
		t.Errorf("a run that did not opt in got a version %d review_result", v)
	}

	opted := durableRepo(t)
	opted.setProtocol(t, 3)
	rc, got, errOut = opted.newEvent(t, "review_started")
	if rc != exitcode.OK {
		t.Fatalf("%d %s", rc, errOut)
	}
	if env := opted.envelopeOf(t, got); env.SchemaVersion != 3 || env.Kind != "review_started" {
		t.Errorf("review_started: %+v", env)
	}
	if b, _ := os.ReadFile(got["payload"].(string)); strings.TrimSpace(string(b)) != "{}" {
		t.Errorf("a start payload is %q: it may only name a session", b)
	}
	rc, got, errOut = opted.newEvent(t, "review_result", "--payload", result)
	if rc != exitcode.OK {
		t.Fatalf("%d %s", rc, errOut)
	}
	if env := opted.envelopeOf(t, got); env.SchemaVersion != 3 || env.Kind != "review_result" {
		t.Errorf("review_result: %+v", env)
	}
	// A builder keeps speaking version 2 on a run that opted in.
	rc, got, errOut = opted.newEvent(t, "builder_started")
	if rc != exitcode.OK {
		t.Fatalf("%d %s", rc, errOut)
	}
	if v := opted.envelopeOf(t, got).SchemaVersion; v != 2 {
		t.Errorf("a builder message was version %d", v)
	}
}

// pinProtocol rewrites the run's pinned configuration to one that opted in.
func (d *durable) pinProtocol(t *testing.T, protocol int) {
	t.Helper()
	id, err := state.ResolveIdentity(d.root, d.home)
	if err != nil {
		t.Fatal(err)
	}
	art, _ := artifact.Open(id.StateRoot)
	h, err := art.Put(context.Background(), strings.NewReader(fmt.Sprintf(`{"review_protocol":%d}`, protocol)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(id.StateRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,'application/json',1)`, h, 20)
	if _, err := db.Exec(`UPDATE runs SET config_hash=? WHERE id=?`, h, d.run); err != nil {
		t.Fatal(err)
	}
}

// Ingest follows the protocol the run was started with. A version 3 message
// for a run that did not opt in is refused as such; for one that did, it is
// judged on its merits (here it names a builder dispatch, which is no review).
func TestReviewHandbackIngestFollowsTheStoredProtocol(t *testing.T) {
	for protocol, want := range map[int]string{2: "unsupported_protocol", 3: "unknown_effect"} {
		d := durableRepo(t)
		d.pinProtocol(t, protocol)
		d.setProtocol(t, 3)
		rc, got, errOut := d.newEvent(t, "review_started")
		if rc != exitcode.OK {
			t.Fatalf("%d %s", rc, errOut)
		}
		rc, res, errOut := d.ingest(t, claim(t, got["queue_file"].(string)))
		if rc != exitcode.OK || res["disposition"] != "quarantined" || !strings.Contains(res["reason"].(string), want) {
			t.Errorf("protocol %d: exit %d %v %s, want a quarantine for %s", protocol, rc, res, errOut, want)
		}
	}
}

// An adapter tandem has no transport for is refused with the mailbox flags
// exactly as it is without them, before anything exists.
func TestReviewHandbackUnsupportedAdapterIsRefusedBeforeAnythingExists(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	durableTransport = defaultDurableTransport
	id := e.createCollaboration()
	rc, _, errOut := e.start("--builder-kind", "nonesuch", "--collaboration", id, "--review-delivery", "mailbox",
		"--acceptance-policy", e.file("policy.json", handbackPolicy))
	if rc == exitcode.OK || !strings.Contains(errOut, "nonesuch") {
		t.Fatalf("an unregistered adapter: %d %s", rc, errOut)
	}
	if n := e.runCount(); n != 0 {
		t.Fatalf("a refused start left %d runs", n)
	}
	if _, err := os.Stat(filepath.Join(e.root, ".tandem-relay", "active-run.json")); err == nil {
		t.Fatal("a refused start left an active-run marker")
	}
}
