package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

// grant gives the current attempt of task a large artifact to read, through the
// evidence the controller records for an attempt.
func (e *cenv) grant(taskID string, content []byte) string {
	e.t.Helper()
	h, err := e.art.Put(ctx, bytes.NewReader(content))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.AdoptArtifact(ctx, h); err != nil {
		e.t.Fatal(err)
	}
	end, _ := e.c.put(ctx, "end snapshot of "+taskID)
	att := e.current(taskID)
	if err := e.st.RecordAttemptEvidence(ctx, att.ID, end, h); err != nil {
		e.t.Fatal(err)
	}
	return h
}

func (e *cenv) read(attemptID, hash string, offset int64, limit int) (ArtifactResult, error) {
	return ReadArtifact(ctx, e.st, e.art, e.runID, attemptID, ArtifactRequest{Hash: hash, Offset: offset, Limit: limit})
}

// otherProject is a second checkout's run with an attempt of its own.
func (e *cenv) otherProject() (attemptID string, packet string) {
	e.t.Helper()
	put := func(s string) string { h, _ := e.c.put(ctx, s); return h }
	spec := state.RunSpec{RunID: strings.Repeat("e", 32), CheckoutID: strings.Repeat("d", 32), CanonicalRoot: "/other", GitCommonDir: "/other/.git", ManifestRoot: ".",
		PlanHash: put("other plan"), ConfigHash: put("other config"),
		Tasks: []state.TaskRow{{ID: "other-task", Ordinal: 1, BriefHash: put("other brief SECRET"), ConstraintsHash: put("c"), SpecHash: put("s")}}}
	if err := e.st.CreateRun(ctx, spec); err != nil {
		e.t.Fatal(err)
	}
	packet = put("the other project's packet SECRET")
	a := state.AttemptSpec{ID: strings.Repeat("f", 32), RunID: spec.RunID, TaskID: "other-task", Ordinal: 1, StartSnapshotHash: put("other start"), PacketHash: packet}
	if err := e.st.CreateAttempt(ctx, a); err != nil {
		e.t.Fatal(err)
	}
	return a.ID, packet
}

func TestArtifactReadRejectsOtherProject(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.current("task-1")
	otherAttempt, otherPacket := e.otherProject()

	for name, c := range map[string]struct{ attempt, hash string }{
		"another project's artifact":         {att.ID, otherPacket},
		"another project's attempt":          {otherAttempt, otherPacket},
		"another project's attempt, ours":    {otherAttempt, att.PacketHash},
		"a registered artifact nothing owns": {att.ID, func() string { h, _ := e.c.put(ctx, "orphan"); return h }()},
		"an artifact that does not exist":    {att.ID, strings.Repeat("9", 64)},
		"the plan":                           {att.ID, func() string { r, _ := e.st.GetRun(ctx, e.runID); return r.PlanHash }()},
		"another task's brief":               {att.ID, e.tasks()[1].BriefHash},
	} {
		res, err := e.read(c.attempt, c.hash, 0, 0)
		if !errors.Is(err, ErrArtifactDenied) {
			t.Errorf("%s: %v", name, err)
		}
		if res.Text != "" || res.DataBase64 != "" || res.TotalBytes != 0 {
			t.Errorf("%s: bytes came back with a refusal: %+v", name, res)
		}
	}
	// A refusal says the same thing whether the artifact exists or not.
	_, a := e.read(att.ID, otherPacket, 0, 0)
	_, b := e.read(att.ID, strings.Repeat("9", 64), 0, 0)
	if strings.ReplaceAll(a.Error(), otherPacket, "X") != strings.ReplaceAll(b.Error(), strings.Repeat("9", 64), "X") {
		t.Errorf("a refusal reveals whether the artifact exists: %q vs %q", a, b)
	}
	// A superseded attempt reads nothing.
	if _, err := e.read(strings.Repeat("0", 32), att.PacketHash, 0, 0); !errors.Is(err, ErrArtifactDenied) {
		t.Errorf("an attempt that does not exist: %v", err)
	}
}

func TestArtifactReadServesWhatTheAttemptOwns(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.current("task-1")
	prompt, _ := e.packet(att)
	mh, _, _ := e.st.AttemptManifest(ctx, att.ID)
	tasks := e.tasks()
	for name, h := range map[string]string{"packet": att.PacketHash, "manifest": mh, "start snapshot": att.StartSnapshotHash,
		"task brief": tasks[0].BriefHash, "task constraints": tasks[0].ConstraintsHash, "task spec": tasks[0].SpecHash} {
		res, err := e.read(att.ID, h, 0, 0)
		if err != nil || res.Hash != h || res.TotalBytes == 0 {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	res, _ := e.read(att.ID, att.PacketHash, 0, 0)
	if res.Encoding != "utf8" || res.Text != prompt || res.NextOffset != nil || res.MediaType != "text/plain; charset=utf-8" || res.Limit != 32768 {
		t.Errorf("%+v", res)
	}
}

func TestArtifactReadChunksAndCaps(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.current("task-1")
	data := bytes.Repeat([]byte("0123456789abcdef"), 5000) // 80000 bytes
	h := e.grant("task-1", data)

	var got []byte
	var offset int64
	for i := 0; ; i++ {
		res, err := e.read(att.ID, h, offset, 100000) // asks for more than the cap
		if err != nil {
			t.Fatal(err)
		}
		if res.Limit != 32768 || len(res.Text) > 32768 || res.TotalBytes != 80000 || res.Offset != offset {
			t.Fatalf("chunk %d: limit %d, %d bytes, total %d, offset %d", i, res.Limit, len(res.Text), res.TotalBytes, res.Offset)
		}
		got = append(got, res.Text...)
		if res.NextOffset == nil {
			break
		}
		offset = *res.NextOffset
		if i > 5 {
			t.Fatal("never finished")
		}
	}
	if !bytes.Equal(got, data) {
		t.Error("the chunks do not add up to the artifact")
	}
	// Edges.
	if res, err := e.read(att.ID, h, 80000, 10); err != nil || res.Text != "" || res.NextOffset != nil || res.Encoding != "utf8" {
		t.Errorf("at the end: %+v %v", res, err)
	}
	for name, c := range map[string]struct {
		off   int64
		limit int
	}{"beyond the end": {80001, 10}, "negative offset": {-1, 10}, "negative limit": {0, -5}} {
		if res, err := e.read(att.ID, h, c.off, c.limit); err == nil || res.Text != "" {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if res, err := e.read(att.ID, h, 5, 4); err != nil || res.Text != "5678" || res.NextOffset == nil || *res.NextOffset != 9 {
		t.Errorf("a small window: %+v %v", res, err)
	}
}

func TestArtifactReadDistinguishesTextFromBinary(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.current("task-1")
	bin := []byte{0xff, 0xfe, 0x00, 0x01, 'a', 'b', 0x80}
	h := e.grant("task-1", bin)
	res, err := e.read(att.ID, h, 0, 0)
	if err != nil || res.Encoding != "base64" || res.Text != "" || res.MediaType != "application/octet-stream" {
		t.Fatalf("%+v %v", res, err)
	}
	if raw, _ := base64.StdEncoding.DecodeString(res.DataBase64); !bytes.Equal(raw, bin) {
		t.Errorf("%v", raw)
	}
	// Valid UTF-8 with a NUL is not text.
	h = e.grant("task-1", []byte("a\x00b"))
	if res, _ := e.read(att.ID, h, 0, 0); res.Encoding != "base64" {
		t.Errorf("%+v", res)
	}
	// A window that ends inside a character stops before it, and the next window starts there.
	h = e.grant("task-1", []byte("日本語"))
	res, err = e.read(att.ID, h, 0, 4)
	if err != nil || res.Encoding != "utf8" || res.Text != "日" || res.NextOffset == nil || *res.NextOffset != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	res, _ = e.read(att.ID, h, 3, 6)
	if res.Text != "本語" || res.NextOffset != nil {
		t.Errorf("%+v", res)
	}
	// The truncated end of a file is not trimmed into text: it is bytes.
	h = e.grant("task-1", []byte("ok\xe6\x97"))
	if res, _ := e.read(att.ID, h, 0, 0); res.Encoding != "base64" {
		t.Errorf("%+v", res)
	}
}

func TestArtifactReadAuthorizesMemoryThroughThePersistedManifest(t *testing.T) {
	e := newCEnv(t)
	e.start()
	scope := []string{"internal/state"}
	shown, _ := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindApprovedDecision, Text: "use sqlite", Scope: scope, Required: true})
	e.accept("task-1", 1)
	att := e.current("task-2")
	// Recorded after the packet was built: not in its manifest.
	late, _ := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindUnresolvedIssue, Text: "late news", Scope: scope})
	se, _, _ := e.st.GetMemory(ctx, shown)
	le, _, _ := e.st.GetMemory(ctx, late)
	if res, err := e.read(att.ID, se.ValueHash, 0, 0); err != nil || res.Text != "use sqlite" {
		t.Errorf("a fact in the manifest: %+v %v", res, err)
	}
	if _, err := e.read(att.ID, le.ValueHash, 0, 0); !errors.Is(err, ErrArtifactDenied) {
		t.Errorf("a fact that was not in the manifest: %v", err)
	}
}

func TestArtifactReadReachesAFactLeftOutForBudget(t *testing.T) {
	e := newCEnv(t)
	// A limit a whole fact cannot fit beside the task: an entry holds at most
	// 8192 bytes, so under the default limit every single fact fits.
	id, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ContextLimit = 8000 }))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = id
	big := strings.Repeat("o", 8000)
	omitted, err := e.c.RecordClaim(ctx, e.runID, ClaimRequest{TaskID: "task-1", Text: big, Scope: []string{"internal/state"}})
	if err != nil {
		t.Fatal(err)
	}
	e.accept("task-1", 1)
	att := e.current("task-2")
	prompt, m := e.packet(att)
	if len(m.Omitted) != 1 || m.Omitted[0].ID != omitted || strings.Contains(prompt, "oooo") {
		t.Fatalf("the fact must have been left out for budget: %+v", m.Omitted)
	}
	// The manifest names it, so its full text can be fetched incrementally.
	entry, _, _ := e.st.GetMemory(ctx, omitted)
	res, err := e.read(att.ID, entry.ValueHash, 0, 0)
	if err != nil || res.Text != big[:len(res.Text)] || res.TotalBytes != int64(len(big)) {
		t.Errorf("%+v %v", res.TotalBytes, err)
	}
}

func TestContextExplainUsesPersistedManifest(t *testing.T) {
	e := newCEnv(t)
	e.start()
	scope := []string{"internal/state"}
	first, _ := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindApprovedDecision, Text: "use sqlite", Scope: scope, Required: true})
	claim, _ := e.c.RecordClaim(ctx, e.runID, ClaimRequest{TaskID: "task-1", Text: "postgres is nicer", Scope: scope})
	e.accept("task-1", 1)
	att := e.current("task-2")

	want, err := ExplainContext(ctx, e.st, e.art, e.runID, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Selected) != 2 || want.Method != "utf8-bytes-upper-estimate-v1" || want.Limit != taskcontext.DefaultMaxEstimatedTokens || want.TaskID != "task-2" || want.Estimated.Total == 0 {
		t.Fatalf("%+v", want)
	}
	byID := map[string]ExplainedFact{}
	for _, f := range want.Selected {
		byID[f.ID] = f
	}
	if f := byID[first]; !f.Required || f.Reason != "direct_dependency" || f.Kind != memory.KindApprovedDecision || f.Trust != memory.TrustApproved || f.ValueHash == "" {
		t.Errorf("%+v", f)
	}
	if f := byID[claim]; f.Required || f.Trust != memory.TrustClaim || f.Kind != memory.KindBuilderClaim {
		t.Errorf("%+v", f)
	}

	// Memory changes after the prompt was sent: the decision is replaced, the claim invalidated.
	if _, err := e.c.RecordDecision(ctx, e.runID, DecisionRequest{TaskID: "task-1", Kind: memory.KindApprovedDecision, Text: "use sqlite, amended", Scope: scope, Required: true, Supersedes: first}); err != nil {
		t.Fatal(err)
	}
	e.st.InvalidateMemory(ctx, e.runID, map[string]bool{})
	got, err := ExplainContext(ctx, e.st, e.art, e.runID, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("the explanation moved with memory:\nwas %s\nnow %s", wj, gj)
	}
	// An attempt with no manifest, and one that is not ours, explain nothing.
	if _, err := ExplainContext(ctx, e.st, e.art, e.runID, strings.Repeat("0", 32)); err == nil {
		t.Error("an unknown attempt")
	}
	otherAttempt, _ := e.otherProject()
	if _, err := ExplainContext(ctx, e.st, e.art, e.runID, otherAttempt); err == nil {
		t.Error("another project's attempt")
	}
}

// requestEnvelope writes what the CLI writes in a sandbox: a payload and an
// artifact_request envelope in the run's queue.
func (e *cenv) request(att workflow.Attempt, taskID, id string, body string, nonce string) string {
	e.t.Helper()
	eff := e.builderEffect(att)
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "builder")
	dir := RunDir(e.relay, e.runID)
	name := "payloads/" + id + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if nonce == "" {
		nonce = eff.Nonce
	}
	sum := sha256.Sum256([]byte(body))
	env := Envelope{SchemaVersion: 2, EventID: id, RunID: e.runID, TaskID: taskID, AttemptID: att.ID, JobID: jobs[0].ID, EffectID: eff.ID, Nonce: nonce,
		Kind: "artifact_request", PayloadPath: name, PayloadHash: hex.EncodeToString(sum[:])}
	raw, _ := json.Marshal(env)
	if err := os.WriteFile(filepath.Join(dir, "q", id+".json"), raw, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return filepath.Join(dir, "responses", id+".json")
}

func readResponse(t *testing.T, path string) ArtifactResponse {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no response: %v", err)
	}
	var r ArtifactResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestArtifactSandboxReadUsesMailbox(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	att := e.current("task-1")
	before, _ := e.st.GetAttempt(ctx, att.ID)
	eventsBefore := len(e.effects(workflow.EffectBuilderDispatch))
	id := strings.Repeat("a", 31) + "1"
	resp := e.request(att, "task-1", id, fmt.Sprintf(`{"hash":%q,"offset":0,"limit":64}`, att.PacketHash), "")
	e.tick()

	r := readResponse(t, resp)
	prompt, _ := e.packet(att)
	if r.Status != "ok" || r.RequestID != id || r.Result == nil || r.Result.Text != prompt[:64] || r.Result.NextOffset == nil || *r.Result.NextOffset != 64 || r.Result.TotalBytes != int64(len(prompt)) {
		t.Fatalf("%+v %+v", r, r.Result)
	}
	// Only the requested window was copied.
	raw, _ := os.ReadFile(resp)
	if len(raw) > 4096 || strings.Contains(string(raw), prompt[200:260]) {
		t.Errorf("the response holds more than the chunk: %d bytes", len(raw))
	}
	// A read is not an event: nothing about the attempt changed.
	after, _ := e.st.GetAttempt(ctx, att.ID)
	if after.Version != before.Version || len(e.effects(workflow.EffectBuilderDispatch)) != eventsBefore {
		t.Error("an artifact read changed the attempt")
	}
	if _, err := os.Stat(filepath.Join(RunDir(e.relay, e.runID), "receipts", id+".json")); err != nil {
		t.Errorf("the request has no receipt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(RunDir(e.relay, e.runID), "q", id+".json")); !os.IsNotExist(err) {
		t.Error("the request is still queued")
	}

	// A hash the attempt may not read is answered with a refusal and no bytes.
	_, otherPacket := e.otherProject()
	id2 := strings.Repeat("b", 31) + "2"
	resp2 := e.request(att, "task-1", id2, fmt.Sprintf(`{"hash":%q,"offset":0,"limit":64}`, otherPacket), "")
	e.tick()
	r2 := readResponse(t, resp2)
	raw2, _ := os.ReadFile(resp2)
	if r2.Status != "refused" || r2.Code != "artifact_not_available" || r2.Result != nil || strings.Contains(string(raw2), "SECRET") {
		t.Errorf("%+v", r2)
	}
}

func TestArtifactRequestsAreHeldToTheBuilderIdentity(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick()
	att := e.current("task-1")
	body := fmt.Sprintf(`{"hash":%q}`, att.PacketHash)
	// Wrong nonce: quarantined, never answered.
	id := strings.Repeat("c", 31) + "3"
	resp := e.request(att, "task-1", id, body, strings.Repeat("0", 32))
	e.tick()
	if _, err := os.Stat(resp); !os.IsNotExist(err) {
		t.Error("a request with the wrong nonce was answered")
	}
	if _, err := os.Stat(filepath.Join(RunDir(e.relay, e.runID), "quarantine", id+".json.taken.reason")); err != nil {
		if _, err2 := os.Stat(filepath.Join(RunDir(e.relay, e.runID), "quarantine", id+".json.reason")); err2 != nil {
			t.Errorf("no quarantine reason: %v", err)
		}
	}
	// Malformed request bodies and oversized windows.
	for name, b := range map[string]string{"not json": "hash please", "unknown field": fmt.Sprintf(`{"hash":%q,"path":"/etc/passwd"}`, att.PacketHash),
		"bad hash": `{"hash":"../../etc/passwd"}`, "negative offset": fmt.Sprintf(`{"hash":%q,"offset":-4}`, att.PacketHash)} {
		id := fmt.Sprintf("%031x%d", len(name), len(b)%10)
		r := e.request(att, "task-1", id, b, "")
		e.tick()
		resp := readResponse(t, r)
		if resp.Status != "refused" || resp.Result != nil {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	// The window cap holds even when the request asks for more.
	big := e.grant("task-1", bytes.Repeat([]byte("x"), 70000))
	id = strings.Repeat("d", 31) + "4"
	r := e.request(att, "task-1", id, fmt.Sprintf(`{"hash":%q,"limit":1000000}`, big), "")
	e.tick()
	if rr := readResponse(t, r); rr.Status != "ok" || len(rr.Result.Text) != 32768 {
		t.Errorf("%+v", rr.Result)
	}
}
