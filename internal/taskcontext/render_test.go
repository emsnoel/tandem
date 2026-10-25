package taskcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/workflow"
)

func hex32(n int) string { return fmt.Sprintf("%032x", n) }
func hex64(n int) string { return fmt.Sprintf("%064x", n) }

func task() workflow.TaskSpec {
	return workflow.TaskSpec{ID: "consumer", Brief: "### Task 3: Render\n\nRender the receipt.", Constraints: "- No new dependencies.\n- Never touch main.",
		DependsOn: []string{"dep"}, Paths: []string{"internal/state"}, Outputs: []string{}}
}

func request(entries ...memory.Entry) Request {
	return Request{RunID: hex32(1), AttemptID: hex32(2), PlanHash: hex64(3), StartSnapshotHash: hex64(4), Task: task(), Entries: entries,
		MaxEstimatedTokens: 8000, Header: "Implement task 3 of 3 (consumer).", Footer: "When you finish, run tandem-handback."}
}

// fact is an optional approved decision by the dependency about internal/state.
func fact(n int, mut ...func(*memory.Entry)) memory.Entry {
	e := memory.Entry{ID: hex32(100 + n), RunID: hex32(1), TaskID: "dep", Kind: memory.KindApprovedDecision, Trust: memory.TrustApproved, State: memory.StateActive,
		Scope: []string{"internal/state"}, ScopeHash: hex64(1000), SourceHash: hex64(2000 + n), ValidityHash: hex64(3000), ValueHash: hex64(4000 + n),
		Value: fmt.Sprintf("decision number %d", n)}
	for _, m := range mut {
		m(&e)
	}
	return e
}

func unrelated(n int) memory.Entry {
	return fact(1000+n, func(e *memory.Entry) {
		e.TaskID = fmt.Sprintf("far-%d", n)
		e.Scope = []string{"cmd/other"}
		e.Required = n%2 == 0
	})
}

func mustBuild(t *testing.T, r Request) Packet {
	t.Helper()
	p, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type parsedManifest struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	AttemptID     string `json:"attempt_id"`
	PlanHash      string `json:"plan_hash"`
	StartSnapshot string `json:"start_snapshot_hash"`
	TaskID        string `json:"task_id"`
	Method        string `json:"estimate_method"`
	Limit         int    `json:"limit"`
	Estimated     struct {
		Required int `json:"required"`
		Optional int `json:"optional"`
		Total    int `json:"total"`
	} `json:"estimated"`
	Sections []struct {
		Name  string `json:"name"`
		Bytes int    `json:"bytes"`
	} `json:"sections"`
	Selected []struct {
		ID       string `json:"id"`
		Reason   string `json:"reason"`
		Required bool   `json:"required"`
	} `json:"selected"`
	Omitted []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"omitted"`
	PromptHash string `json:"prompt_hash"`
	ConfigHash string `json:"config_hash"`
}

func decode(t *testing.T, p Packet) parsedManifest {
	t.Helper()
	var m parsedManifest
	if err := json.Unmarshal(p.Manifest, &m); err != nil {
		t.Fatalf("manifest: %v\n%s", err, p.Manifest)
	}
	return m
}

func TestBuildRendersTheMandatoryPartsFirstAndInOrder(t *testing.T) {
	p := mustBuild(t, request(fact(1)))
	order := []string{"Implement task 3 of 3 (consumer).", "run " + hex32(1), "No new dependencies.", "### Task 3: Render", "decision number 1", "tandem-handback"}
	at := -1
	for _, want := range order {
		i := strings.Index(p.Prompt, want)
		if i < 0 || i < at {
			t.Fatalf("%q is missing or out of order (at %d, after %d) in:\n%s", want, i, at, p.Prompt)
		}
		at = i
	}
	if !strings.Contains(p.Prompt, hex64(3)[:12]) || !strings.Contains(p.Prompt, hex64(4)[:12]) {
		t.Errorf("the refs must carry the plan and start snapshot:\n%s", p.Prompt)
	}
	if p.EstimateMethod != "utf8-bytes-upper-estimate-v1" || p.EstimatedTokens != len(p.Prompt) {
		t.Errorf("method %q, estimate %d, bytes %d", p.EstimateMethod, p.EstimatedTokens, len(p.Prompt))
	}
	if !strings.HasSuffix(p.Prompt, "When you finish, run tandem-handback.\n") {
		t.Errorf("the footer must close the prompt:\n%q", p.Prompt[len(p.Prompt)-60:])
	}
}

func TestManifestRecordsWhatWasSelectedAndWhy(t *testing.T) {
	req := request(fact(1), fact(2, func(e *memory.Entry) { e.Required = true }))
	p := mustBuild(t, req)
	m := decode(t, p)
	if m.SchemaVersion != 1 || m.RunID != req.RunID || m.AttemptID != req.AttemptID || m.PlanHash != req.PlanHash || m.StartSnapshot != req.StartSnapshotHash || m.TaskID != "consumer" {
		t.Errorf("identity %+v", m)
	}
	if m.Method != EstimateMethod || m.Limit != 8000 {
		t.Errorf("method %q limit %d", m.Method, m.Limit)
	}
	sum := sha256.Sum256([]byte(p.Prompt))
	if m.PromptHash != hex.EncodeToString(sum[:]) || len(m.ConfigHash) != 64 {
		t.Errorf("hashes %q %q", m.PromptHash, m.ConfigHash)
	}
	if m.Estimated.Total != len(p.Prompt) || m.Estimated.Required+m.Estimated.Optional != m.Estimated.Total || m.Estimated.Required == 0 || m.Estimated.Optional == 0 {
		t.Errorf("estimates %+v for %d bytes", m.Estimated, len(p.Prompt))
	}
	total := 0
	for _, s := range m.Sections {
		total += s.Bytes
	}
	if total != len(p.Prompt) {
		t.Errorf("sections add up to %d, prompt is %d", total, len(p.Prompt))
	}
	if len(m.Selected) != 2 || m.Selected[0].ID != hex32(102) || !m.Selected[0].Required || m.Selected[1].Required || len(m.Omitted) != 0 {
		t.Errorf("selected %+v omitted %+v", m.Selected, m.Omitted)
	}
	if !json.Valid(p.Manifest) || strings.Contains(string(p.Manifest), "T1") {
		t.Error("manifest")
	}
}

func TestContextUnaffectedByUnrelatedHistory(t *testing.T) {
	base := []memory.Entry{fact(1), fact(2, func(e *memory.Entry) { e.Required = true })}
	want := mustBuild(t, request(base...))
	var sizes []int
	for _, n := range []int{1, 20, 100} {
		entries := append([]memory.Entry{}, base...)
		for i := 0; i < n; i++ {
			entries = append(entries, unrelated(i))
		}
		got := mustBuild(t, request(entries...))
		if got.Prompt != want.Prompt || string(got.Manifest) != string(want.Manifest) {
			t.Fatalf("%d unrelated entries changed the packet", n)
		}
		sizes = append(sizes, got.EstimatedTokens)
	}
	if sizes[0] != sizes[1] || sizes[1] != sizes[2] {
		t.Errorf("sizes %v", sizes)
	}
}

func TestContextRequiredOverflowRefusesDispatch(t *testing.T) {
	fit := mustBuild(t, request())
	req := request()
	req.MaxEstimatedTokens = fit.EstimatedTokens
	if _, err := Build(req); err != nil {
		t.Fatalf("a packet exactly at the limit must fit: %v", err)
	}
	req.MaxEstimatedTokens = fit.EstimatedTokens - 1
	p, err := Build(req)
	var big *TooLargeError
	if !errors.Is(err, workflow.ErrContextTooLarge) || !errors.As(err, &big) || p.Prompt != "" || p.Manifest != nil {
		t.Fatalf("one byte over: %v %+v", err, p)
	}
	if big.Limit != req.MaxEstimatedTokens || big.Required != fit.EstimatedTokens || len(big.Sections) == 0 {
		t.Errorf("%+v", big)
	}
	names := map[string]int{}
	for _, s := range big.Sections {
		names[s.Name] = s.Bytes
	}
	for _, want := range []string{"header", "refs", "constraints", "brief", "footer"} {
		if names[want] == 0 {
			t.Errorf("the error must give the size of %s: %v", want, big.Sections)
		}
	}
	if !strings.Contains(err.Error(), "constraints") {
		t.Errorf("the message must name sections: %v", err)
	}

	// One huge mandatory constraint is refused, never trimmed.
	req = request(fact(1))
	req.Task.Constraints = strings.Repeat("Never do this. ", 1000)
	if _, err := Build(req); !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Errorf("%v", err)
	}
	// A required fact counts as mandatory.
	req = request(fact(1, func(e *memory.Entry) { e.Required = true; e.Value = strings.Repeat("must ", 3000) }))
	if _, err := Build(req); !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Errorf("a huge required fact: %v", err)
	}
	// And the wrapper counts: the header and footer are mandatory text.
	req = request()
	req.Header = strings.Repeat("h", 9000)
	if _, err := Build(req); !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Errorf("a huge header: %v", err)
	}
}

func TestContextWholeOptionalFactOrOmission(t *testing.T) {
	small := fact(1)
	base := mustBuild(t, request(small))
	// perFact is the size of one fact block whose value is a single byte.
	probe := mustBuild(t, request(small, fact(2, func(e *memory.Entry) { e.Value = "x" })))
	perFact := probe.EstimatedTokens - base.EstimatedTokens - 0
	// The room above the base packet: enough for the notice and a small fact.
	room := len(omissionNotice) + perFact + 10
	withBlock := func(size int) memory.Entry { // a fact whose block is exactly size bytes
		return fact(2, func(e *memory.Entry) { e.Value = strings.Repeat("x", size-perFact+1) })
	}
	limit := base.EstimatedTokens + room

	// A fact that fits whole in the room is shown, and no notice is needed.
	fits := withBlock(room)
	req := request(small, fits)
	req.MaxEstimatedTokens = limit
	p := mustBuild(t, req)
	if m := decode(t, p); len(m.Omitted) != 0 || len(m.Selected) != 2 || p.EstimatedTokens != limit {
		t.Fatalf("a fact that fits exactly: %d bytes of %d, %+v", p.EstimatedTokens, limit, m)
	}
	if strings.Contains(p.Prompt, "tandem task context") {
		t.Error("a prompt that left nothing out has no notice")
	}

	// One byte more and it is left out entirely, with the notice.
	big := withBlock(room + 1)
	req = request(small, big)
	req.MaxEstimatedTokens = limit
	p = mustBuild(t, req)
	m := decode(t, p)
	if strings.Contains(p.Prompt, "xxx") {
		t.Fatal("a fact that did not fit was partly rendered")
	}
	if len(m.Selected) != 1 || len(m.Omitted) != 1 || m.Omitted[0].ID != big.ID || m.Omitted[0].Reason != "over_budget" {
		t.Fatalf("selected %+v omitted %+v", m.Selected, m.Omitted)
	}
	if !strings.Contains(p.Prompt, "tandem task context") {
		t.Errorf("a prompt that left facts out must say how to see what:\n%s", p.Prompt)
	}
	if p.EstimatedTokens > limit {
		t.Errorf("%d bytes over a limit of %d", p.EstimatedTokens, limit)
	}

	// The notice is part of the packet, so room for it is kept back: a fact that
	// would fit only by squeezing the notice out is left out, and the packet
	// stays within the limit.
	mid := fact(3, func(e *memory.Entry) { e.Value = strings.Repeat("y", room-5-perFact+1) })
	req = request(small, big, mid)
	req.MaxEstimatedTokens = limit
	p = mustBuild(t, req)
	if p.EstimatedTokens > limit || strings.Contains(p.Prompt, "yyy") {
		t.Errorf("%d bytes for a limit of %d; mid fact shown: %v", p.EstimatedTokens, limit, strings.Contains(p.Prompt, "yyy"))
	}
	if m := decode(t, p); len(m.Omitted) != 2 {
		t.Errorf("both facts are left out: %+v", m.Omitted)
	}

	// A smaller fact later in order still gets in after a big one is skipped.
	small2 := fact(3, func(e *memory.Entry) { e.Value = "tiny" })
	req = request(small, big, small2)
	req.MaxEstimatedTokens = limit
	m = decode(t, mustBuild(t, req))
	got := map[string]bool{}
	for _, s := range m.Selected {
		got[s.ID] = true
	}
	if !got[small2.ID] || got[big.ID] || len(m.Omitted) != 1 {
		t.Errorf("selected %+v omitted %+v", m.Selected, m.Omitted)
	}
}

func TestContextHugeOptionalFactIsOmittedNotTruncated(t *testing.T) {
	huge := fact(1, func(e *memory.Entry) { e.Value = strings.Repeat("a", 20000) })
	p := mustBuild(t, request(huge, fact(2)))
	if strings.Contains(p.Prompt, "aaaa") || !strings.Contains(p.Prompt, "decision number 2") {
		t.Fatal("the huge fact leaked in, or the small one was lost")
	}
	m := decode(t, p)
	if len(m.Omitted) != 1 || m.Omitted[0].ID != huge.ID {
		t.Errorf("%+v", m.Omitted)
	}
}

func TestContextDeterministicForInputPermutation(t *testing.T) {
	var entries []memory.Entry
	for i := 1; i <= 12; i++ {
		entries = append(entries, fact(i, func(e *memory.Entry) {
			if i%3 == 0 {
				e.Required = true
				e.Scope = []string{fmt.Sprintf("internal/state/%d", i)} // distinct scope: no conflict
			}
			if i%4 == 0 {
				e.TaskID = "other"
				e.Scope = []string{"internal/state/x"}
			}
		}))
	}
	want := mustBuild(t, request(entries...))
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 25; i++ {
		shuffled := append([]memory.Entry{}, entries...)
		r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := mustBuild(t, request(shuffled...))
		if got.Prompt != want.Prompt || string(got.Manifest) != string(want.Manifest) {
			t.Fatalf("permutation %d changed the packet", i)
		}
	}
	// Identical input, identical output; the request is not modified.
	again := mustBuild(t, request(entries...))
	if again.Prompt != want.Prompt || string(again.Manifest) != string(want.Manifest) || entries[0].ID != hex32(101) {
		t.Error("not deterministic")
	}
}

func TestBytesAreCountedNotRunes(t *testing.T) {
	req := request(fact(1, func(e *memory.Entry) { e.Value = "日本語のメモ — naïve café 🙂" }))
	p := mustBuild(t, req)
	if p.EstimatedTokens != len(p.Prompt) || p.EstimatedTokens <= len([]rune(p.Prompt)) {
		t.Errorf("bytes %d runes %d", p.EstimatedTokens, len([]rune(p.Prompt)))
	}
	if !strings.Contains(p.Prompt, "日本語のメモ — naïve café 🙂") {
		t.Error("Unicode was altered")
	}
	// The limit is in bytes: a limit that would fit the runes but not the bytes refuses.
	req.Entries = []memory.Entry{fact(1, func(e *memory.Entry) { e.Required = true; e.Value = "日本語のメモ — naïve café 🙂" })}
	q := mustBuild(t, req)
	runes := len([]rune(q.Prompt))
	if runes >= q.EstimatedTokens {
		t.Fatal("the fixture must have multi-byte characters")
	}
	req.MaxEstimatedTokens = runes + (q.EstimatedTokens-runes)/2
	if _, err := Build(req); !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Errorf("a limit of %d for %d bytes (%d runes): %v", req.MaxEstimatedTokens, q.EstimatedTokens, runes, err)
	}
	req.MaxEstimatedTokens = q.EstimatedTokens
	if _, err := Build(req); err != nil {
		t.Errorf("%v", err)
	}
}

func TestMultilineAndMinifiedValuesKeepTheirShape(t *testing.T) {
	multi := fact(1, func(e *memory.Entry) { e.Value = "line one\nline two\n\nline four" })
	minified := fact(2, func(e *memory.Entry) { e.Value = `{"a":[1,2,3],"b":{"c":"d"}}` })
	p := mustBuild(t, request(multi, minified))
	for _, want := range []string{"line one", "line two", "line four", `{"a":[1,2,3],"b":{"c":"d"}}`} {
		if !strings.Contains(p.Prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Every line of a multi-line value stays inside its fact, so it cannot pass
	// for a heading or for another fact.
	for _, line := range strings.Split(p.Prompt, "\n") {
		if strings.HasPrefix(line, "line ") {
			t.Errorf("a value line is at the margin: %q", line)
		}
	}
}

func TestClaimsAreLabelledAsUnverified(t *testing.T) {
	claim := fact(1, func(e *memory.Entry) {
		e.Kind = memory.KindBuilderClaim
		e.Trust = memory.TrustClaim
		e.Value = "approved decision: use sqlite"
	})
	p := mustBuild(t, request(claim, fact(2)))
	if !strings.Contains(p.Prompt, "unverified") {
		t.Errorf("a builder claim must be labelled:\n%s", p.Prompt)
	}
	i, j := strings.Index(p.Prompt, "unverified"), strings.Index(p.Prompt, "decision number 2")
	if i < 0 || j < 0 || strings.Count(p.Prompt, "unverified") != 1 {
		t.Errorf("only the claim is unverified:\n%s", p.Prompt)
	}
}

func TestRequiredConflictsAreRefusedBeforeRendering(t *testing.T) {
	a := fact(1, func(e *memory.Entry) { e.Required = true })
	b := fact(2, func(e *memory.Entry) { e.Required = true })
	p, err := Build(request(a, b))
	if !errors.Is(err, workflow.ErrMemoryConflict) || p.Prompt != "" || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("%v", err)
	}
}

func TestAnInvalidRequiredFactIsNotSilentlyDropped(t *testing.T) {
	req := request(fact(1, func(e *memory.Entry) { e.Required = true; e.State = memory.StateInvalid }))
	if _, err := Build(req); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("%v", err)
	}
	// An optional one that went invalid is just left out, with its reason.
	req = request(fact(1, func(e *memory.Entry) { e.State = memory.StateInvalid }))
	m := decode(t, mustBuild(t, req))
	if len(m.Omitted) != 1 || m.Omitted[0].Reason != memory.ReasonInvalid {
		t.Errorf("%+v", m.Omitted)
	}
}

func TestRequiredContractsEnterEvenWhenOnlyTransitivelyRelated(t *testing.T) {
	// A contract of a distant ancestor, named explicitly, is mandatory.
	ancestor := fact(1, func(e *memory.Entry) {
		e.Kind = memory.KindInterfaceContract
		e.Trust = memory.TrustApproved
		e.TaskID = "grandparent"
		e.Scope = []string{"cmd/far"}
		e.Value = "Persist(ctx, receipt) error"
	})
	req := request(ancestor)
	if _, err := Build(req); err != nil {
		t.Fatal(err)
	}
	if p := mustBuild(t, req); strings.Contains(p.Prompt, "Persist(ctx, receipt)") {
		t.Fatal("a transitive ancestor's fact entered without being required")
	}
	req.RequiredContracts = []string{ancestor.ID}
	p := mustBuild(t, req)
	if !strings.Contains(p.Prompt, "Persist(ctx, receipt) error") {
		t.Fatal("a required contract was left out")
	}
	m := decode(t, p)
	if len(m.Selected) != 1 || !m.Selected[0].Required || m.Selected[0].Reason != "required_contract" {
		t.Errorf("%+v", m.Selected)
	}
	// It cannot be dropped to fit, even when optional facts would have room.
	req.MaxEstimatedTokens = p.EstimatedTokens
	if _, err := Build(req); err != nil {
		t.Errorf("%v", err)
	}
	req.MaxEstimatedTokens = p.EstimatedTokens - 1
	if _, err := Build(req); !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Errorf("%v", err)
	}
	for name, bad := range map[string][]string{
		"not supplied": {hex32(999)},
		"duplicate":    {ancestor.ID, ancestor.ID},
	} {
		r := request(ancestor)
		r.RequiredContracts = bad
		_, err := Build(r)
		if name == "not supplied" && !errors.Is(err, workflow.ErrIncompleteEvidence) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "duplicate" && err != nil {
			t.Errorf("%s: naming a contract twice is harmless: %v", name, err)
		}
	}
	notContract := fact(2)
	r := request(notContract)
	r.RequiredContracts = []string{notContract.ID}
	if _, err := Build(r); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Errorf("only an interface contract can be required by name: %v", err)
	}
	gone := ancestor
	gone.State = memory.StateSuperseded
	r = request(gone)
	r.RequiredContracts = []string{gone.ID}
	if _, err := Build(r); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Errorf("a required contract that was superseded: %v", err)
	}
}

func TestBuildRejectsWhatItCannotRenderFaithfully(t *testing.T) {
	for name, mut := range map[string]func(*Request){
		"invalid utf8 in the brief":       func(r *Request) { r.Task.Brief = "a\xffb" },
		"invalid utf8 in the constraints": func(r *Request) { r.Task.Constraints = "a\xffb" },
		"invalid utf8 in a fact":          func(r *Request) { r.Entries = []memory.Entry{fact(1, func(e *memory.Entry) { e.Value = "a\xffb" })} },
		"invalid utf8 in the header":      func(r *Request) { r.Header = "a\xffb" },
		"empty brief":                     func(r *Request) { r.Task.Brief = "  \n" },
		"bad run id":                      func(r *Request) { r.RunID = "x" },
		"bad attempt id":                  func(r *Request) { r.AttemptID = "" },
		"bad plan hash":                   func(r *Request) { r.PlanHash = "abc" },
		"bad snapshot hash":               func(r *Request) { r.StartSnapshotHash = "" },
		"bad task id":                     func(r *Request) { r.Task.ID = "Bad Id" },
		"negative limit":                  func(r *Request) { r.MaxEstimatedTokens = -1 },
	} {
		r := request(fact(1))
		mut(&r)
		if p, err := Build(r); err == nil || p.Prompt != "" {
			t.Errorf("%s: accepted", name)
		}
	}
	// Zero means the default.
	r := request()
	r.MaxEstimatedTokens = 0
	if m := decode(t, mustBuild(t, r)); m.Limit != DefaultMaxEstimatedTokens || DefaultMaxEstimatedTokens != 24000 {
		t.Errorf("limit %d", m.Limit)
	}
	// An empty constraint section is left out, not rendered as a heading with nothing under it.
	r = request()
	r.Task.Constraints = ""
	if p := mustBuild(t, r); strings.Contains(strings.ToLower(p.Prompt), "constraints") {
		t.Errorf("an empty section was rendered:\n%s", p.Prompt)
	}
}

func TestConfigHashChangesWithTheConfiguration(t *testing.T) {
	a := decode(t, mustBuild(t, request()))
	r := request()
	r.MaxEstimatedTokens = 4000
	b := decode(t, mustBuild(t, r))
	if a.ConfigHash == b.ConfigHash || a.PromptHash != b.PromptHash {
		t.Errorf("config %s %s, prompt %s %s", a.ConfigHash, b.ConfigHash, a.PromptHash, b.PromptHash)
	}
}
