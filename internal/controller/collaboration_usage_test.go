package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/state"
)

// fakeLogs stands in for the providers' log directories: each session's log is
// a real file the test appends to, as an agent would.
type fakeLogs struct {
	mu       sync.Mutex
	dir      string
	children map[string][]rollout.Found
	absent   map[string]bool
}

func newFakeLogs(t *testing.T) *fakeLogs {
	return &fakeLogs{dir: t.TempDir(), children: map[string][]rollout.Found{}, absent: map[string]bool{}}
}

func (f *fakeLogs) file(session string) string { return filepath.Join(f.dir, session+".jsonl") }

func (f *fakeLogs) Path(_ context.Context, _, session string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.absent[session] {
		return "", nil
	}
	if _, err := os.Stat(f.file(session)); err != nil {
		return "", nil
	}
	return f.file(session), nil
}

func (f *fakeLogs) Children(_ context.Context, _, session string, _, _ time.Time) ([]rollout.Found, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.children[session], nil
}

func (f *fakeLogs) append(t *testing.T, session string, lines ...string) {
	t.Helper()
	fh, err := os.OpenFile(f.file(session), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, l := range lines {
		fmt.Fprintln(fh, l)
	}
}

func codexTotal(in, cached, out int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":0}}}}`, in, cached, out)
}

const (
	codexMeta     = `{"type":"session_meta","payload":{"id":"%s"}}`
	codexComplete = `{"type":"event_msg","payload":{"type":"task_complete"}}`
)

func (f *fakeLogs) codexSession(t *testing.T, id string, totals ...string) {
	f.append(t, id, append([]string{fmt.Sprintf(codexMeta, id)}, totals...)...)
}

// collabEnvWithLogs starts the env's run in a non-enforcing collaboration with
// session logs the test controls. The builder's session is the one the fake
// transport reports for its dispatch.
func (e *cenv) collabEnvWithLogs(t *testing.T) (*fakeLogs, string) {
	t.Helper()
	logs := newFakeLogs(t)
	e.logs = logs
	e.c = e.newController()
	e.startInCollaboration()
	e.tickUntilSent(1)
	return logs, "session-" + e.tr.sends[0].EffectID[:6]
}

func (e *cenv) snapshot(t *testing.T) CollaborationReport {
	t.Helper()
	rep, err := e.c.ReportCollaboration(ctx, testCollab)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestCollectionAddsNothingOnRepeat(t *testing.T) {
	e := newCEnv(t)
	logs, session := e.collabEnvWithLogs(t)
	logs.codexSession(t, session, codexTotal(100, 60, 10), codexTotal(150, 90, 15))
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	first := e.snapshot(t)
	for i := 0; i < 3; i++ {
		if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
			t.Fatal(err)
		}
	}
	rep := e.snapshot(t)
	if got := rep.Snapshot.Totals["codex"].InputTotal; got == nil || *got != 150 {
		t.Fatalf("totals after repeated collection: %+v", rep.Snapshot.Totals)
	}
	if rep.Record.ID != first.Record.ID {
		t.Fatalf("repeating collection made revision %d after %d: it must add nothing", rep.Record.Revision, first.Record.Revision)
	}
}

func TestCollectionReadsOnlyWhatIsNew(t *testing.T) {
	e := newCEnv(t)
	logs, session := e.collabEnvWithLogs(t)
	logs.codexSession(t, session, codexTotal(100, 60, 10))
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	before := e.snapshot(t).Record
	_, firstOffset, _, _ := e.st.UsageCursor(ctx, "codex", session)
	logs.append(t, session, codexTotal(180, 100, 14))
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	_, secondOffset, _, _ := e.st.UsageCursor(ctx, "codex", session)
	if secondOffset <= firstOffset {
		t.Fatalf("cursor %d -> %d", firstOffset, secondOffset)
	}
	rep := e.snapshot(t)
	// The second total is differenced from the first across the two reads.
	if got := *rep.Snapshot.Totals["codex"].InputTotal; got != 180 || rep.Record.Revision != before.Revision+1 {
		t.Fatalf("input %d at revision %d (was %d)", got, rep.Record.Revision, before.Revision)
	}
	old, err := e.st.UsageSnapshot(ctx, before.ID)
	if err != nil || old.SupersededBy != rep.Record.ID {
		t.Fatalf("the old snapshot was not invalidated: %+v %v", old, err)
	}
}

func TestSnapshotMissingAndTruncatedLogsStayLowerBounds(t *testing.T) {
	e := newCEnv(t)
	logs, session := e.collabEnvWithLogs(t)
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	rep := e.snapshot(t)
	if rep.Snapshot.Complete || len(rep.Snapshot.Totals) != 0 || !strings.Contains(strings.Join(rep.Snapshot.Reasons, " "), "missing_source") {
		t.Fatalf("a log that does not exist yet: %+v", rep.Snapshot)
	}
	logs.codexSession(t, session, codexTotal(100, 60, 10), codexTotal(150, 90, 15))
	e.c.CollectCollaboration(ctx, testCollab)
	if got := *e.snapshot(t).Snapshot.Totals["codex"].InputTotal; got != 150 {
		t.Fatalf("input %d", got)
	}
	// The log is cut back below what was read: the stored usage stays.
	if err := os.WriteFile(logs.file(session), []byte(fmt.Sprintf(codexMeta, session)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.c.CollectCollaboration(ctx, testCollab)
	rep = e.snapshot(t)
	if got := rep.Snapshot.Totals["codex"].InputTotal; got == nil || *got != 150 || rep.Snapshot.Complete ||
		!strings.Contains(strings.Join(rep.Snapshot.Reasons, " "), "source_truncated") {
		t.Fatalf("a truncated log became zero: %+v %v", rep.Snapshot.Totals, rep.Snapshot.Reasons)
	}
	logs.absent[session] = true
	e.c.CollectCollaboration(ctx, testCollab)
	if got := *e.snapshot(t).Snapshot.Totals["codex"].InputTotal; got != 150 {
		t.Fatalf("a vanished log became %d", got)
	}
}

func TestArchiveIsProvisionalWhileAChildRuns(t *testing.T) {
	e := newCEnv(t)
	logs, session := e.collabEnvWithLogs(t)
	logs.codexSession(t, session, codexTotal(100, 60, 10), codexComplete)
	logs.children[session] = []rollout.Found{{Path: logs.file("cx-child"), SessionID: "cx-child", ParentID: session}}
	logs.codexSession(t, "cx-child", codexTotal(40, 0, 4)) // still running: no terminal event
	e.e0Settle(t)
	// The run is archived while the child is still going.
	e.c.CollectRun(ctx, e.runID)
	rep := e.snapshot(t)
	if rep.Snapshot.Complete {
		t.Fatal("a snapshot with a running child was complete")
	}
	child := false
	for _, s := range rep.Snapshot.ExpectedSources {
		if s.Role == "subagent" {
			child = true
			if s.Final {
				t.Fatalf("the running child is final: %+v", s)
			}
		}
	}
	if !child {
		t.Fatalf("the child was not discovered: %+v", rep.Snapshot.ExpectedSources)
	}
	// The child finishes after the archive and keeps its original owner.
	logs.append(t, "cx-child", codexComplete)
	if err := e.c.CloseCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	rep = e.snapshot(t)
	if !rep.Snapshot.Complete || *rep.Snapshot.Totals["codex"].InputTotal != 140 {
		t.Fatalf("after the child finished: complete %v %+v %v", rep.Snapshot.Complete, rep.Snapshot.Totals, rep.Snapshot.Reasons)
	}
	if status, _ := e.st.CollaborationStatus(ctx, testCollab); status != "closed" {
		t.Fatalf("status %s", status)
	}
}

func TestCorrectionAfterACompleteSnapshotMakesANewRevision(t *testing.T) {
	e := newCEnv(t)
	logs, session := e.collabEnvWithLogs(t)
	logs.codexSession(t, session, codexTotal(100, 60, 10), codexComplete)
	e.e0Settle(t)
	if err := e.c.CloseCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	first := e.snapshot(t)
	if !first.Snapshot.Complete {
		t.Fatalf("not complete: %v", first.Snapshot.Reasons)
	}
	// The session's log then gains a late correction: more usage after its
	// terminal event. The old snapshot is invalidated and the new one is not final.
	logs.append(t, session, codexTotal(130, 70, 12))
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	second := e.snapshot(t)
	if second.Record.Revision != first.Record.Revision+1 || second.Record.Supersedes != first.Record.ID {
		t.Fatalf("revision %d supersedes %q", second.Record.Revision, second.Record.Supersedes)
	}
	if old, _ := e.st.UsageSnapshot(ctx, first.Record.ID); old.SupersededBy != second.Record.ID {
		t.Fatalf("the complete snapshot was not invalidated: %+v", old)
	}
	if got := *second.Snapshot.Totals["codex"].InputTotal; got != 130 {
		t.Fatalf("corrected input %d", got)
	}
}

func TestCloseFreezesLaunchesBeforeAnyPaidCloseout(t *testing.T) {
	e := newCEnv(t)
	e.collabEnvWithLogs(t)
	if err := e.c.CloseCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
	status, _ := e.st.CollaborationStatus(ctx, testCollab)
	if status != "closing" {
		t.Fatalf("a collaboration with an unsettled builder closed: %s", status)
	}
	before := e.sent()
	e.ticks(4)
	if e.sent() != before {
		t.Fatalf("%d sends after the freeze", e.sent()-before)
	}
	// Collection and reporting still work.
	if err := e.c.CollectCollaboration(ctx, testCollab); err != nil {
		t.Fatalf("collection after the freeze: %v", err)
	}
	if _, err := e.c.ReportCollaboration(ctx, testCollab); err != nil {
		t.Fatal(err)
	}
}

func TestSettlementFromCollectedUsageReleasesTheReservation(t *testing.T) {
	e := newCEnv(t)
	logs := newFakeLogs(t)
	e.logs = logs
	e.c = e.newController()
	e.startEnforced(1000)
	e.tickUntilSent(1)
	session := "session-" + e.tr.sends[0].EffectID[:6]
	if b := e.codexBudget(); b.InputReserved != 60 {
		t.Fatalf("reserved %+v", b)
	}
	logs.codexSession(t, session, codexTotal(70, 30, 6), codexComplete)
	e.e0Settle(t)
	e.c.CollectRun(ctx, e.runID)
	b := e.codexBudget()
	if b.InputReserved != 0 || b.InputSpent != 70 || b.OutputSpent != 6 {
		t.Fatalf("after settlement: %+v", b)
	}
	if flight, _ := e.st.InFlightAdmissions(ctx, enforcedCollab); len(flight) != 0 {
		t.Fatalf("still in flight: %+v", flight)
	}
}

// e0Settle ends the builder job, as an accepted attempt does.
func (e *cenv) e0Settle(t *testing.T) {
	t.Helper()
	if _, err := e.raw().Exec(`UPDATE jobs SET state='completed' WHERE kind='builder'`); err != nil {
		t.Fatal(err)
	}
}

var _ = state.ErrNoSource
