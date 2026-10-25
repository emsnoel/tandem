package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// mailboxEnv is a real store with one run, one pending attempt and its
// builder dispatch effect, plus a mailbox directory.
type mailboxEnv struct {
	st      *state.Store
	art     *artifact.Store
	stRoot  string
	dir     string // <relay>/durable/<run>
	deps    MailboxDeps
	run     string
	attempt string
	job     string
	effect  string
	nonce   string
}

func newMailboxEnv(t *testing.T) *mailboxEnv {
	t.Helper()
	st, stRoot := openState(t)
	art, err := artifact.Open(stRoot)
	if err != nil {
		t.Fatal(err)
	}
	e := &mailboxEnv{st: st, art: art, stRoot: stRoot, dir: filepath.Join(t.TempDir(), "relay", "durable", hex32(201)),
		run: hex32(201), attempt: hex32(202), job: hex32(203), effect: hex32(204), nonce: hex32(206)}
	for _, d := range []string{"payloads", "q", "receipts", "quarantine"} {
		if err := os.MkdirAll(filepath.Join(e.dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(stRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	put := func(s string) string {
		h, err := art.Put(ctx, strings.NewReader(s))
		if err != nil {
			t.Fatal(err)
		}
		db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,'text/plain',1)`, h, len(s))
		return h
	}
	plan, cfg, brief, packet, start, ev := put("plan"), put("config"), put("brief"), put("packet"), put("start"), put("dispatch event")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?, '/c', '/c/.git', 1)`, []any{hex32(210)}},
		{`INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?, '.')`, []any{hex32(211), hex32(210)}},
		{`INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,created_ms) VALUES(?,?,?,?,2,'running',1)`, []any{e.run, hex32(211), plan, cfg}},
		{`INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?, 'receipt-store',1,?,?,?)`, []any{e.run, brief, brief, brief}},
		{`INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,start_snapshot_hash,packet_hash,version,is_current,created_ms)
			VALUES(?,?, 'receipt-store',1,'pending','pending','pending','available',?,?,1,1,1)`, []any{e.attempt, e.run, start, packet}},
		{`INSERT INTO events(id,run_id,attempt_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?,?, 'attempt_created',?,0,1,1)`, []any{hex32(212), e.run, e.attempt, ev}},
		{`INSERT INTO effects(id,run_id,attempt_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms) VALUES(?,?,?,?, 'builder_dispatch',0,?,?, 'delivering',1,1,1)`,
			[]any{e.effect, e.run, e.attempt, hex32(212), packet, e.nonce}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %q: %v", q.sql, err)
		}
	}
	e.deps = MailboxDeps{State: st, Artifacts: art, Dir: e.dir, RunID: e.run}
	return e
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// envelope writes a payload under payloads/ and returns a valid v2 envelope for it.
func (e *mailboxEnv) envelope(t *testing.T, kind, payload string, mutate func(*Envelope)) []byte {
	t.Helper()
	name := "payloads/" + kind + ".json"
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Envelope{SchemaVersion: 2, EventID: hex32(300 + len(kind)), RunID: e.run, TaskID: "receipt-store", AttemptID: e.attempt,
		JobID: e.job, EffectID: e.effect, Nonce: e.nonce, Kind: kind, PayloadPath: name, PayloadHash: sha(payload)}
	if mutate != nil {
		mutate(&env)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (e *mailboxEnv) counts(t *testing.T) (events, effects int, impl, effectState string) {
	t.Helper()
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(e.stRoot, "state.sqlite"))
	defer db.Close()
	db.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
	db.QueryRow(`SELECT count(*) FROM effects`).Scan(&effects)
	db.QueryRow(`SELECT implementation_state FROM attempts WHERE id=?`, e.attempt).Scan(&impl)
	db.QueryRow(`SELECT state FROM effects WHERE id=?`, e.effect).Scan(&effectState)
	return
}

func code(t *testing.T, err error) string {
	t.Helper()
	var rej *RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want a *RejectError", err)
	}
	return rej.Code
}

const startPayload = "{\"message\":\"started\"}\n"

func TestMailboxStartAcknowledgesThePromptAndStartsTheBuilder(t *testing.T) {
	e := newMailboxEnv(t)
	r, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderStarted, startPayload, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.Duplicate || r.Version != 2 {
		t.Fatalf("receipt %+v", r)
	}
	events, effects, impl, effState := e.counts(t)
	if events != 2 || effects != 1 || impl != "running" || effState != "acknowledged" {
		t.Fatalf("events %d effects %d implementation %s effect %s", events, effects, impl, effState)
	}
	// The payload became an immutable artifact before the event referenced it.
	if raw, err := e.art.ReadAll(ctx, sha(startPayload), 1<<20); err != nil || string(raw) != startPayload {
		t.Fatalf("payload artifact %q %v", raw, err)
	}
}

func TestMailboxReplayReturnsTheOriginalReceipt(t *testing.T) {
	e := newMailboxEnv(t)
	env := e.envelope(t, workflow.EventBuilderStarted, startPayload, nil)
	first, err := IngestMailbox(ctx, e.deps, env)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := IngestMailbox(ctx, e.deps, env)
		if err != nil || !again.Duplicate || again.Version != first.Version {
			t.Fatalf("replay %d: %+v %v", i, again, err)
		}
	}
	if events, _, _, _ := e.counts(t); events != 2 {
		t.Fatalf("%d events", events)
	}
}

func TestMailboxCannotForgeControllerEvent(t *testing.T) {
	e := newMailboxEnv(t)
	before := func() [2]int { ev, ef, _, _ := e.counts(t); return [2]int{ev, ef} }()
	for _, kind := range []string{
		workflow.EventAcceptRequested, workflow.EventCheckCompleted, workflow.EventReviewCompleted, workflow.EventWaiveRequested,
		workflow.EventBudgetChanged, workflow.EventStopRequested, workflow.EventRepairStarted, workflow.EventAttemptCreated,
	} {
		_, err := IngestMailbox(ctx, e.deps, e.envelope(t, kind, "{}\n", nil))
		if got := code(t, err); got != "controller_only_event" {
			t.Errorf("%s: code %s", kind, got)
		}
	}
	if _, err := IngestMailbox(ctx, e.deps, e.envelope(t, "made_up", "{}\n", nil)); code(t, err) != "unsupported_kind" {
		t.Errorf("an unknown kind: %v", err)
	}
	if ev, ef, impl, effState := e.counts(t); [2]int{ev, ef} != before || impl != "pending" || effState != "delivering" {
		t.Fatalf("a forged event changed state: events %d effects %d %s %s", ev, ef, impl, effState)
	}
}

func TestMailboxWrongRunAndAttempt(t *testing.T) {
	e := newMailboxEnv(t)
	for name, tc := range map[string]struct {
		mutate func(*Envelope)
		want   string
	}{
		"an old queue copied to a new run":     {func(en *Envelope) { en.RunID = hex32(999) }, "wrong_run"},
		"an attempt that does not exist":       {func(en *Envelope) { en.AttemptID = hex32(0) }, "stale_attempt"},
		"another run's attempt":                {func(en *Envelope) { en.AttemptID = hex32(998) }, "stale_attempt"},
		"a wrong nonce":                        {func(en *Envelope) { en.Nonce = hex32(1) }, "wrong_nonce"},
		"an effect that is not this attempt's": {func(en *Envelope) { en.EffectID = hex32(997) }, "unknown_effect"},
	} {
		_, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderStarted, startPayload, tc.mutate))
		if got := code(t, err); got != tc.want {
			t.Errorf("%s: code %s, want %s", name, got, tc.want)
		}
	}
	if ev, _, impl, effState := e.counts(t); ev != 1 || impl != "pending" || effState != "delivering" {
		t.Fatalf("a rejected message advanced something: events %d %s %s", ev, impl, effState)
	}

	// A repaired attempt is superseded: its old nonce-bound message is stale.
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(e.stRoot, "state.sqlite"))
	db.Exec(`UPDATE attempts SET is_current=0 WHERE id=?`, e.attempt)
	db.Close()
	if _, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderStarted, startPayload, nil)); code(t, err) != "stale_attempt" {
		t.Fatalf("a superseded attempt: %v", err)
	}
}

func TestMailboxRejectsMalformedEnvelopes(t *testing.T) {
	e := newMailboxEnv(t)
	good := e.envelope(t, workflow.EventBuilderStarted, startPayload, nil)
	cases := map[string]struct {
		raw  []byte
		want string
	}{
		"schema 3":             {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.SchemaVersion = 3 }), "unsupported_protocol"},
		"schema 1 (legacy v1)": {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.SchemaVersion = 1 }), "unsupported_protocol"},
		"not json":             {[]byte("task=1"), "malformed_envelope"},
		"a v1 task message":    {[]byte(`{"id":"x","kind":"task","from":"builder","task":1,"stuck":false,"report_path":"/tmp/r.md"}`), "malformed_envelope"},
		"unknown field":        {bytes.Replace(good, []byte(`"kind"`), []byte(`"extra":1,"kind"`), 1), "malformed_envelope"},
		"trailing data":        {append(append([]byte{}, good...), []byte(`{"x":1}`)...), "malformed_envelope"},
		"envelope too large":   {bytes.Repeat([]byte(" "), MaxEnvelopeBytes+1), "malformed_envelope"},
		"short event id":       {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.EventID = "abc" }), "invalid_id"},
		"event id with a path": {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.EventID = "../../" + hex32(1)[:26] }), "invalid_id"},
		"missing nonce":        {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.Nonce = "" }), "invalid_id"},
		"bad task id":          {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.TaskID = "Bad Task!" }), "invalid_id"},
		"bad hash":             {e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.PayloadHash = "zz" }), "invalid_id"},
	}
	for name, tc := range cases {
		_, err := IngestMailbox(ctx, e.deps, tc.raw)
		if got := code(t, err); got != tc.want {
			t.Errorf("%s: code %s, want %s", name, got, tc.want)
		}
	}
	if ev, _, _, _ := e.counts(t); ev != 1 {
		t.Fatalf("%d events", ev)
	}
}

func TestMailboxRejectsUnsafePayloadPaths(t *testing.T) {
	e := newMailboxEnv(t)
	for _, p := range []string{
		"../../outside", "/etc/passwd", "payloads/../../x", "payloads//a.json", "payloads/", "", "other/x.json",
		"payloads/a\x00b", "./payloads/x.json", "payloads/./x.json", "payloads/sub/../x.json", strings.Repeat("payloads/", 40) + "x",
	} {
		_, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.PayloadPath = p }))
		if got := code(t, err); got != "invalid_payload_path" {
			t.Errorf("path %q: code %s", p, got)
		}
	}
}

// Validation and open are two steps; a path swapped for a symlink between them
// must not be followed.
func TestMailboxPayloadSymlinkSwap(t *testing.T) {
	secret := "OUTSIDE SECRET " + hex32(4242)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "submit.json"), []byte(secret), 0o644)

	swaps := map[string]func(e *mailboxEnv){
		"the file becomes a symlink": func(e *mailboxEnv) {
			p := filepath.Join(e.dir, "payloads", "builder_started.json")
			os.Remove(p)
			os.Symlink(filepath.Join(outside, "submit.json"), p)
		},
		"a directory component becomes a symlink": func(e *mailboxEnv) {
			os.Mkdir(filepath.Join(e.dir, "payloads", "nested"), 0o755)
		},
		"the payloads directory becomes a symlink": func(e *mailboxEnv) {
			real := filepath.Join(e.dir, "payloads")
			os.Rename(real, real+".moved")
			os.Symlink(outside, real)
			os.WriteFile(filepath.Join(outside, "builder_started.json"), []byte(secret), 0o644)
		},
	}
	for name, swap := range swaps {
		t.Run(name, func(t *testing.T) {
			e := newMailboxEnv(t)
			raw := e.envelope(t, workflow.EventBuilderStarted, startPayload, nil)
			e.deps.beforeOpen = func() { swap(e) }
			if name == "a directory component becomes a symlink" {
				// The envelope names a nested path whose directory is swapped.
				raw = e.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.PayloadPath = "payloads/nested/x.json" })
				e.deps.beforeOpen = func() {
					os.Symlink(outside, filepath.Join(e.dir, "payloads", "nested"))
					os.WriteFile(filepath.Join(outside, "x.json"), []byte(secret), 0o644)
				}
			}
			_, err := IngestMailbox(ctx, e.deps, raw)
			if err == nil {
				t.Fatal("a swapped path was imported")
			}
			if got := code(t, err); got != "invalid_payload_path" {
				t.Fatalf("code %s: %v", got, err)
			}
			if ev, _, impl, _ := e.counts(t); ev != 1 || impl != "pending" {
				t.Fatalf("state advanced: events %d %s", ev, impl)
			}
			filepath.Walk(e.stRoot, func(p string, info os.FileInfo, err error) error {
				if err == nil && info.Mode().IsRegular() {
					if body, _ := os.ReadFile(p); bytes.Contains(body, []byte(secret)) {
						t.Errorf("%s holds the outside file's bytes", p)
					}
				}
				return nil
			})
		})
	}
}

func TestMailboxPayloadLimitsAndHash(t *testing.T) {
	e := newMailboxEnv(t)
	big := strings.Repeat("a", MaxPayloadBytes+1)
	_, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderStarted, big, nil))
	if code(t, err) != "payload_too_large" {
		t.Fatalf("oversized payload: %v", err)
	}
	exactly := strings.Repeat("b", MaxPayloadBytes)
	if _, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderStarted, exactly, nil)); err != nil {
		t.Fatalf("a payload of exactly the limit: %v", err)
	}
	e2 := newMailboxEnv(t)
	_, err = IngestMailbox(ctx, e2.deps, e2.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.PayloadHash = sha("some other bytes") }))
	if code(t, err) != "payload_hash_mismatch" {
		t.Fatalf("hash mismatch: %v", err)
	}
	if ev, _, _, _ := e2.counts(t); ev != 1 {
		t.Fatal("a mismatched payload advanced state")
	}
	if _, err := e2.art.ReadAll(ctx, sha("some other bytes"), 100); err == nil {
		t.Fatal("a mismatched payload was stored under the claimed hash")
	}
	// A directory or a FIFO is not a payload.
	os.Mkdir(filepath.Join(e2.dir, "payloads", "adir"), 0o755)
	_, err = IngestMailbox(ctx, e2.deps, e2.envelope(t, workflow.EventBuilderStarted, startPayload, func(en *Envelope) { en.PayloadPath = "payloads/adir" }))
	if code(t, err) != "invalid_payload_path" {
		t.Fatalf("a directory as a payload: %v", err)
	}
}

func TestMailboxOutOfOrderAndDuplicateSubmission(t *testing.T) {
	e := newMailboxEnv(t)
	e.deps.Observed = func(context.Context, Envelope) workflow.Observed {
		return workflow.Observed{SessionSettled: true, SchedulingAllowed: true}
	}
	end := sha("end snapshot")
	patch := sha("patch")
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(e.stRoot, "state.sqlite")+"?_pragma=foreign_keys(1)")
	for _, h := range []string{end, patch} {
		art, _ := e.art.Put(ctx, strings.NewReader(map[string]string{end: "end snapshot", patch: "patch"}[h]))
		db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,5,'t',1)`, art)
	}
	db.Exec(`UPDATE attempts SET end_snapshot_hash=?, patch_hash=? WHERE id=?`, end, patch, e.attempt)
	db.Close()

	// The submission arrives first, with a job that was never created.
	_, err := IngestMailbox(ctx, e.deps, e.envelope(t, workflow.EventBuilderSubmitted, "{\"done\":true}\n", nil))
	if code(t, err) != "unknown_job" {
		t.Fatalf("a submission for a job that does not exist: %v", err)
	}
	db, _ = sql.Open("sqlite", "file:"+filepath.Join(e.stRoot, "state.sqlite")+"?_pragma=foreign_keys(1)")
	cfg := sha("config")
	db.Exec(`INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?, 'builder','running',?,?, 'test')`, e.job, e.attempt, cfg, cfg)
	db.Close()

	sub := e.envelope(t, workflow.EventBuilderSubmitted, "{\"done\":true}\n", nil)
	if _, err := IngestMailbox(ctx, e.deps, sub); err != nil {
		t.Fatalf("the submission: %v", err)
	}
	// Same content, new event id: a second submission is not a second event.
	second := e.envelope(t, workflow.EventBuilderSubmitted, "{\"done\":true}\n", func(en *Envelope) { en.EventID = hex32(399) })
	if _, err := IngestMailbox(ctx, e.deps, second); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a duplicate submission: %v", err)
	}
	// A late start acknowledgement after the submission changes nothing.
	late := e.envelope(t, workflow.EventBuilderStarted, startPayload, nil)
	if _, err := IngestMailbox(ctx, e.deps, late); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a start after the submission: %v", err)
	}
	if _, _, impl, _ := e.counts(t); impl != "submitted" {
		t.Fatalf("implementation %s", impl)
	}
}

func TestMailboxReviewResultIsNotAppliedYet(t *testing.T) {
	e := newMailboxEnv(t)
	_, err := IngestMailbox(ctx, e.deps, e.envelope(t, "review_result", "{}\n", nil))
	if !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("review_result: %v", err)
	}
	if ev, _, _, _ := e.counts(t); ev != 1 {
		t.Fatal("an unsupported kind changed state")
	}
}

// A crash after the commit but before the claimed file is removed must not
// double-apply: reingestion finds the receipt.
func TestMailboxReplayTakenAfterCommit(t *testing.T) {
	e := newMailboxEnv(t)
	claimed := filepath.Join(e.dir, "q", hex32(300+len(workflow.EventBuilderStarted))+".json.taken")
	if err := os.WriteFile(claimed, e.envelope(t, workflow.EventBuilderStarted, startPayload, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("bridge died before removing the claimed file")
	e.deps.afterReceipt = func() error { return crash }
	if _, err := ProcessClaimed(ctx, e.deps, claimed); !errors.Is(err, crash) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(claimed); err != nil {
		t.Fatal("the crash must leave the claimed file behind")
	}
	if ev, ef, impl, _ := e.counts(t); ev != 2 || ef != 1 || impl != "running" {
		t.Fatalf("after the crash: events %d effects %d %s", ev, ef, impl)
	}
	receiptPath := filepath.Join(e.dir, "receipts", hex32(300+len(workflow.EventBuilderStarted))+".json")
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("the durable receipt must exist before the claimed file goes: %v", err)
	}

	e.deps.afterReceipt = nil
	res, err := ProcessClaimed(ctx, e.deps, claimed)
	if err != nil || res.Disposition != Duplicate || res.Receipt.Version != 2 {
		t.Fatalf("replay: %+v %v", res, err)
	}
	if _, err := os.Stat(claimed); !os.IsNotExist(err) {
		t.Fatal("the claimed file was not removed after the replay")
	}
	if ev, ef, _, _ := e.counts(t); ev != 2 || ef != 1 {
		t.Fatalf("one event and one effect expected, got %d and %d", ev, ef)
	}
}

func TestProcessClaimedQuarantinesPermanentRejectionsWithAReason(t *testing.T) {
	e := newMailboxEnv(t)
	claimed := filepath.Join(e.dir, "q", "forged.json.taken")
	os.WriteFile(claimed, e.envelope(t, workflow.EventAcceptRequested, "{}\n", nil), 0o644)
	res, err := ProcessClaimed(ctx, e.deps, claimed)
	if err != nil || res.Disposition != Quarantined || res.Reason != "controller_only_event" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(claimed); !os.IsNotExist(err) {
		t.Fatal("a rejected message stayed in the queue")
	}
	reason, err := os.ReadFile(filepath.Join(e.dir, "quarantine", "forged.json.taken.reason"))
	if err != nil || !strings.Contains(string(reason), "controller_only_event") || len(reason) > 512 {
		t.Fatalf("reason %q %v", reason, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "quarantine", "forged.json.taken")); err != nil {
		t.Fatal("the evidence was not preserved")
	}
	// Garbage and oversize files are quarantined too, never requeued.
	for name, body := range map[string][]byte{"junk.json.taken": []byte("not json"), "huge.json.taken": bytes.Repeat([]byte("x"), MaxEnvelopeBytes*4)} {
		p := filepath.Join(e.dir, "q", name)
		os.WriteFile(p, body, 0o644)
		if res, err := ProcessClaimed(ctx, e.deps, p); err != nil || res.Disposition != Quarantined {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if ev, _, _, _ := e.counts(t); ev != 1 {
		t.Fatal("quarantined messages changed state")
	}
}

func TestProcessClaimedLeavesTransientFailuresForRetry(t *testing.T) {
	e := newMailboxEnv(t)
	claimed := filepath.Join(e.dir, "q", "later.json.taken")
	os.WriteFile(claimed, e.envelope(t, workflow.EventBuilderStarted, startPayload, nil), 0o644)
	e.deps.State.Close() // the store is unavailable: nothing here is the message's fault
	res, err := ProcessClaimed(ctx, e.deps, claimed)
	if err == nil || res.Disposition == Quarantined {
		t.Fatalf("a store failure must not quarantine a good message: %+v %v", res, err)
	}
	if _, serr := os.Stat(claimed); serr != nil {
		t.Fatal("the claimed file must stay for a retry")
	}
}

func TestActiveRunMarkerAndManifest(t *testing.T) {
	relay := filepath.Join(t.TempDir(), ".tandem-relay")
	if got, err := ReadActiveRun(relay); err != nil || got != nil {
		t.Fatalf("no marker: %+v %v", got, err)
	}
	m := RunManifest{SchemaVersion: 2, RunID: hex32(201), Tasks: []ManifestTask{{Ordinal: 1, ID: "receipt-store"}, {Ordinal: 2, ID: "next-one"}}}
	dir, err := InitRunDir(relay, ActiveRun{RunID: hex32(201), ProjectID: hex32(211)}, m)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(relay, "durable", hex32(201)) {
		t.Fatalf("dir %s", dir)
	}
	got, err := ReadActiveRun(relay)
	if err != nil || got == nil || got.EngineVersion != 2 || got.ProtocolVersion != 2 || got.RunID != hex32(201) || got.ProjectID != hex32(211) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, sub := range []string{"q", "payloads", "receipts", "quarantine"} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			t.Errorf("%s: %v", sub, err)
		}
	}
	if id, err := ResolveTask(dir, 2); err != nil || id != "next-one" {
		t.Fatalf("task 2 -> %q %v", id, err)
	}
	for _, n := range []int{0, 3, -1} {
		if _, err := ResolveTask(dir, n); err == nil {
			t.Errorf("task %d resolved", n)
		}
	}
	// A durable run has no legacy build record: nothing may fake one.
	if _, err := os.Stat(filepath.Join(relay, "build", "build.json")); !os.IsNotExist(err) {
		t.Fatal("a legacy build.json appeared beside a durable run")
	}
	// A corrupt marker is an error, never "no marker".
	os.WriteFile(filepath.Join(relay, ActiveRunFile), []byte("{broken"), 0o644)
	if _, err := ReadActiveRun(relay); err == nil {
		t.Fatal("a corrupt marker read as absent")
	}
}
