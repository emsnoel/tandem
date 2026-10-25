package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// fakeUsage stands in for the agent CLIs' logs: what is filed for each session.
type fakeUsage struct {
	mu          sync.Mutex
	streams     map[string][]rollout.Stream
	unsupported map[string]bool
	calls       []string
	from, to    time.Time
	// prompts is the session whose log holds each marker, for SessionWith.
	prompts map[string]string
}

func (f *fakeUsage) SessionWith(_ context.Context, kind, marker string, from, to time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsupported[kind] {
		return "", fmt.Errorf("%w: no session search for %s", workflow.ErrUnsupported, kind)
	}
	return f.prompts[marker], nil
}

func (f *fakeUsage) Collect(_ context.Context, kind, session string, from, to time.Time) ([]rollout.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind+"/"+session)
	f.from, f.to = from, to
	if f.unsupported[kind] {
		return nil, fmt.Errorf("%w: no usage source for %s", workflow.ErrUnsupported, kind)
	}
	return f.streams[session], nil
}

// ev is one logged event, with the id a real log would give it.
func ev(session string, line int, in, cached, out int64) rollout.Delta {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", session, line)))
	return rollout.Delta{SourceID: hex.EncodeToString(sum[:16]), Line: line, Model: "gpt-test", Counters: rollout.Counters{Input: in, Cached: cached, Output: out}}
}

func stream(session, parent string, deltas ...rollout.Delta) rollout.Stream {
	return rollout.Stream{Provider: "codex", SessionID: session, ParentID: parent, File: session + ".jsonl", Deltas: deltas}
}

// sessionOf is the session id the fake transport reports for a task's attempt.
func (e *cenv) sessionOf(att workflow.Attempt) string {
	return "session-" + e.builderEffect(att).ID[:6]
}

func usageEnv(t *testing.T) (*cenv, *fakeUsage) {
	t.Helper()
	e := newCEnv(t)
	u := &fakeUsage{streams: map[string][]rollout.Stream{}, unsupported: map[string]bool{}}
	e.usage = u
	e.c = e.newController()
	return e, u
}

func (e *cenv) total(t *testing.T) workflow.Usage {
	t.Helper()
	u, err := e.c.CollectUsage(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsageBeforeAnyModelJobIsZeroNotUnknown(t *testing.T) {
	e, _ := usageEnv(t)
	e.start()
	// Nothing has been sent: no model has been asked, so nothing has been used.
	u := e.total(t)
	if u.Completeness != workflow.UsageComplete || u.Input == nil || *u.Input != 0 || u.Output == nil {
		t.Errorf("%+v", u)
	}
}

func TestCollectUsagePersistsEachSourceOnce(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	e.tick()
	s := e.sessionOf(e.current("task-1"))
	src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 2, 100, 40, 5), ev(s, 3, 50, 10, 2))}

	u := e.total(t)
	if *u.Input != 150 || *u.CachedInput != 50 || *u.Output != 7 || u.Completeness != workflow.UsageComplete || u.InputSemantics != workflow.SemanticsIncludesCache {
		t.Fatalf("%+v", u)
	}
	if v, ok := u.Uncached(); !ok || v != 100 {
		t.Errorf("uncached %d %v", v, ok)
	}
	rows, _ := e.st.ListUsage(ctx, e.runID)
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	// Collecting again, or from another process, counts nothing twice.
	for i := 0; i < 3; i++ {
		if again := e.total(t); *again.Input != 150 {
			t.Fatalf("collection %d: %+v", i, again)
		}
	}
	if rows, _ := e.st.ListUsage(ctx, e.runID); len(rows) != 2 {
		t.Errorf("%d rows after repeated collection", len(rows))
	}
	// The log grows: only the new event is added.
	src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 2, 100, 40, 5), ev(s, 3, 50, 10, 2), ev(s, 4, 10, 0, 1))}
	if grown := e.total(t); *grown.Input != 160 || *grown.Output != 8 {
		t.Errorf("%+v", grown)
	}
	if rows, _ := e.st.ListUsage(ctx, e.runID); len(rows) != 3 {
		t.Errorf("%d rows", len(rows))
	}
	// The search covers the whole run, not just today.
	if src.from.IsZero() || src.from.After(src.to) {
		t.Errorf("interval %v .. %v", src.from, src.to)
	}
	if len(src.calls) == 0 || src.calls[0] != "codex/"+s {
		t.Errorf("calls %v", src.calls)
	}
}

func TestUsageMissingChildRemainsPartialThroughTheController(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	e.tick()
	s := e.sessionOf(e.current("task-1"))
	child := rollout.Stream{Provider: "codex", SessionID: "child-1", ParentID: s, File: "child.jsonl", Unavailable: "line 3 does not decode"}
	src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 2, 100, 40, 5)), child}

	u := e.total(t)
	if u.Completeness != workflow.UsagePartial || u.Input == nil || *u.Input != 100 {
		t.Fatalf("a lower bound, marked partial: %+v", u)
	}
	if _, ok := u.Uncached(); ok {
		t.Error("an uncached figure from a partial total")
	}
	rep, err := e.c.UsageReport(ctx, e.runID, false)
	if err != nil || rep.ByRole["builder"].Completeness != workflow.UsagePartial || !strings.Contains(strings.Join(rep.Notes, " "), "child-1") {
		t.Errorf("%+v %v", rep, err)
	}
	// The gap is on record, and closes when the log can be read.
	if rows, _ := e.st.ListUsage(ctx, e.runID); len(rows) != 2 {
		t.Errorf("%d rows: the event and the gap marker", len(rows))
	}
	src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 2, 100, 40, 5)), stream("child-1", s, ev("child-1", 2, 20, 0, 3))}
	u = e.total(t)
	if u.Completeness != workflow.UsageComplete || *u.Input != 120 || *u.Output != 8 {
		t.Errorf("%+v", u)
	}
	if rows, _ := e.st.ListUsage(ctx, e.runID); len(rows) != 2 {
		t.Errorf("%d rows: the stale gap marker must be gone", len(rows))
	}
}

func TestUsageTruncatedLogIsPartialUntilItIsComplete(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	e.tick()
	s := e.sessionOf(e.current("task-1"))
	cut := stream(s, "", ev(s, 2, 100, 40, 5))
	cut.Truncated = true
	src.streams[s] = []rollout.Stream{cut}
	if u := e.total(t); u.Completeness != workflow.UsagePartial || *u.Input != 100 {
		t.Fatalf("%+v", u)
	}
	src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 2, 100, 40, 5), ev(s, 3, 20, 0, 1))}
	if u := e.total(t); u.Completeness != workflow.UsageComplete || *u.Input != 120 {
		t.Errorf("%+v", u)
	}
}

func TestUsageNeverCountsAnAggregateAndItsChildren(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	e.tick()
	s := e.sessionOf(e.current("task-1"))
	parent := stream(s, "", ev(s, 2, 300, 0, 30))
	parent.IncludesChildren = true
	src.streams[s] = []rollout.Stream{parent, stream("child-1", s, ev("child-1", 2, 100, 0, 10))}
	if u := e.total(t); *u.Input != 300 || *u.Output != 30 {
		t.Errorf("%+v", u)
	}
	if rows, _ := e.st.ListUsage(ctx, e.runID); len(rows) != 1 {
		t.Errorf("%d rows: the covered child must not be stored", len(rows))
	}
}

func TestUsageUnsupportedAdapterIsUnavailableNotZero(t *testing.T) {
	e, src := usageEnv(t)
	src.unsupported["copilot"] = true
	run, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.BuilderKind, r.BuilderSession, r.PlannerSession, r.ShareContext = "copilot", "b", "p", true
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = run
	e.tick()
	u := e.total(t)
	if u.Completeness != workflow.UsageUnavailable || u.Input != nil || u.Output != nil || u.CachedInput != nil {
		t.Fatalf("an adapter with no usage source: %+v", u)
	}
	rep, _ := e.c.UsageReport(ctx, e.runID, false)
	if !strings.Contains(strings.Join(rep.Notes, " "), "copilot") {
		t.Errorf("%v", rep.Notes)
	}
}

func TestUsageIsAttributedToBuilderAndRepairSeparately(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	finding := workflow.Finding{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "wrong"}
	first := e.requestChanges("task-1", 1, finding)
	second := e.current("task-1")
	s1, s2 := e.sessionOf(first), e.sessionOf(second)
	src.streams[s1] = []rollout.Stream{stream(s1, "", ev(s1, 2, 100, 40, 5))}
	src.streams[s2] = []rollout.Stream{stream(s2, "", ev(s2, 2, 30, 10, 2), ev(s2, 3, 20, 0, 1))}

	rep, err := e.c.UsageReport(ctx, e.runID, false)
	if err != nil {
		t.Fatal(err)
	}
	b, r := rep.ByRole["builder"], rep.ByRole["repair"]
	if *b.Input != 100 || *b.Output != 5 || *r.Input != 50 || *r.Output != 3 || *rep.Total.Input != 150 || *rep.Total.Output != 8 {
		t.Errorf("builder %+v repair %+v total %+v", b, r, rep.Total)
	}
	// A review that ran in a pane shares the builder's session, so it cannot be
	// told apart there, and the report says so rather than showing zero.
	rv := rep.ByRole["reviewer"]
	if rv.Completeness != workflow.UsageUnavailable || rv.Input != nil || !strings.Contains(strings.Join(rep.Notes, " "), "pane") {
		t.Errorf("reviewer %+v notes %v", rv, rep.Notes)
	}
	if _, has := rep.ByRole["judge"]; has {
		t.Error("a durable run has no judge usage to report")
	}
}

func TestUsageExportCarriesSourceProviderModelAndConfigIdentity(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	e.tick()
	att := e.current("task-1")
	s := e.sessionOf(att)
	src.streams[s] = []rollout.Stream{stream(s, "", ev(s, 2, 100, 40, 5))}
	rep, err := e.c.UsageReport(ctx, e.runID, true)
	if err != nil || len(rep.Records) != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	r := rep.Records[0]
	run, _ := e.st.GetRun(ctx, e.runID)
	if r.SourceID == "" || r.Provider != "codex" || r.Model != "gpt-test" || r.ConfigHash == "" || r.Role != "builder" || r.AttemptID != att.ID || r.TaskID != "task-1" || *r.Input != 100 {
		t.Errorf("%+v", r)
	}
	_ = run
	if rep2, _ := e.c.UsageReport(ctx, e.runID, false); len(rep2.Records) != 0 {
		t.Error("records are exported only when asked for")
	}
}

func TestUsageSourceErrorsAreAGapNotAFailure(t *testing.T) {
	e, _ := usageEnv(t)
	e.usage = errSource{errors.New("the sessions directory is not readable")}
	e.c = e.newController()
	e.start()
	e.tick()
	u := e.total(t)
	if u.Completeness != workflow.UsageUnavailable || u.Input != nil {
		t.Errorf("%+v", u)
	}
}

type errSource struct{ err error }

func (e errSource) Collect(context.Context, string, string, time.Time, time.Time) ([]rollout.Stream, error) {
	return nil, e.err
}

// Two jobs can name one log: a pane whose conversation was not restarted, or an
// id Herdr reported late. The lines are counted once, for the job that claimed
// them first, and the other job says so. The report is not refused.
func TestUsageLogNamedByTwoJobsIsCountedOnce(t *testing.T) {
	e, src := usageEnv(t)
	e.start()
	e.accept("task-1", 1)
	s1, s2 := e.sessionOf(e.current("task-1")), e.sessionOf(e.current("task-2"))
	shared := stream("shared-conversation", "", ev("shared-conversation", 2, 100, 40, 5), ev("shared-conversation", 3, 50, 10, 2))
	src.streams[s1], src.streams[s2] = []rollout.Stream{shared}, []rollout.Stream{shared}

	u, err := e.c.CollectUsage(ctx, e.runID)
	if err != nil {
		t.Fatalf("a log named by two jobs failed the whole report: %v", err)
	}
	if u.Input == nil || *u.Input != 150 || *u.Output != 7 {
		t.Errorf("the shared log must count once: %+v", u)
	}
	rep, err := e.c.UsageReport(ctx, e.runID, false)
	if err != nil || !strings.Contains(strings.Join(rep.Notes, " "), "already counted for") {
		t.Errorf("the second job does not say its log was already counted: %v %v", rep.Notes, err)
	}
	// Asking again changes nothing.
	if again, err := e.c.CollectUsage(ctx, e.runID); err != nil || *again.Input != 150 {
		t.Errorf("a second collection: %+v %v", again, err)
	}
	if rows, _ := e.st.ListUsage(ctx, e.runID); len(rows) != 2 {
		t.Errorf("%d rows", len(rows))
	}
}

// The run reads each builder's usage from its own CLI's logs: Codex rollouts
// or Claude Code conversations. Without Claude's, a Claude builder's usage was
// unknown and a task budget refused its first review.
func TestAgentUsageSourceReadsEachKindsLogs(t *testing.T) {
	projects := t.TempDir()
	session := "57312883-6ae0-44d2-8053-3429d17e95c0"
	log := filepath.Join(projects, "-repo", session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","uuid":"u1","sessionId":"` + session + `","message":{"id":"m1","model":"claude-sonnet-5-5","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":40}}}` + "\n"
	if err := os.WriteFile(log, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	src := AgentUsageSource{CodexSessions: t.TempDir(), ClaudeProjects: projects}
	streams, err := src.Collect(context.Background(), "claude", session, time.Time{}, time.Now())
	if err != nil || len(streams) != 1 || len(streams[0].Deltas) != 1 || streams[0].Deltas[0].Counters.Input != 1102 {
		t.Fatalf("claude: %+v %v", streams, err)
	}
	if streams, err := src.Collect(context.Background(), "codex", "no-such-session", time.Time{}, time.Now()); err != nil || len(streams) == 0 || streams[0].Unavailable == "" {
		t.Errorf("codex: a missing rollout is a gap: %+v %v", streams, err)
	}
	if _, err := src.Collect(context.Background(), "opencode", session, time.Time{}, time.Now()); !errors.Is(err, workflow.ErrUnsupported) {
		t.Errorf("a kind with no log reader: %v", err)
	}
}

// A usage row's provider is the kind of the agent that ran its job: the
// reviewer pane's kind for a reviewer row, never the builder's.
func TestUsageProviderFollowsTheRowsRole(t *testing.T) {
	cfg := runConfig{BuilderKind: "copilot", ReviewerKind: "codex"}
	for _, c := range []struct {
		row  state.UsageRow
		want string
	}{
		{state.UsageRow{JobKind: "builder", AttemptOrdinal: 1}, "copilot"},
		{state.UsageRow{JobKind: "builder", AttemptOrdinal: 2}, "copilot"},
		{state.UsageRow{JobKind: "reviewer", AttemptOrdinal: 1}, "codex"},
	} {
		if got := cfg.usageProvider(usageRole(c.row)); got != c.want {
			t.Errorf("%s row (attempt %d): provider %q, want %q", c.row.JobKind, c.row.AttemptOrdinal, got, c.want)
		}
	}
}
