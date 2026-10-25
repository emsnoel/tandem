package herdr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func asError(err error, target **Error) bool { return errors.As(err, target) }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "events", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeEventAcceptsBothSpellings(t *testing.T) {
	ev, ok, err := decodeEvent(fixture(t, "status_changed.jsonl"))
	if err != nil || !ok || ev.Kind != "pane_agent_status_changed" || ev.PaneID != "w8:p30" || ev.Status != "blocked" {
		t.Fatalf("dotted: %+v ok=%v err=%v", ev, ok, err)
	}
	ev, ok, err = decodeEvent(fixture(t, "updated.jsonl"))
	if err != nil || !ok || ev.Kind != "pane_updated" || ev.Status != "idle" || ev.Session != "sess-9" {
		t.Fatalf("underscored: %+v ok=%v err=%v", ev, ok, err)
	}
}

func TestDecodeEventReadsReleaseExitAndMove(t *testing.T) {
	ev, _, _ := decodeEvent(fixture(t, "released.jsonl"))
	if !ev.Released || !ev.Gone() || ev.PaneID != "w8:p30" {
		t.Errorf("release: %+v", ev)
	}
	ev, _, _ = decodeEvent(fixture(t, "exited.jsonl"))
	if ev.Kind != "pane_exited" || !ev.Gone() {
		t.Errorf("exit: %+v", ev)
	}
	ev, _, _ = decodeEvent(fixture(t, "moved.jsonl"))
	if ev.Kind != "pane_moved" || ev.PaneID != "w9:p1" || ev.PrevPaneID != "w8:p30" || ev.Gone() {
		t.Errorf("move: %+v", ev)
	}
}

func TestDecodeEventSkipsUnknownKindsAndRejectsGarbage(t *testing.T) {
	if _, ok, err := decodeEvent(fixture(t, "unknown.jsonl")); ok || err != nil {
		t.Errorf("unknown kind: ok=%v err=%v, want skipped", ok, err)
	}
	_, _, err := decodeEvent([]byte(`{"event":`))
	var he *Error
	if !asError(err, &he) || he.Code != "bad_event" {
		t.Errorf("garbage: err=%v, want bad_event", err)
	}
}

func agentAt(name, pane, status string) *Agent {
	return &Agent{PaneID: pane, Name: &name, Kind: "claude", Status: &status}
}

func watchFake(t *testing.T, s *fakeServer, snap map[string]*Agent, names ...string) *AgentWatch {
	t.Helper()
	w, err := watchOn(s.dial, func(n string) (*Agent, error) { return snap[n], nil }, 8, names...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestWatchSubscribesToEveryKindItUses(t *testing.T) {
	s := newFakeServer(t)
	watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder")
	got := map[string]bool{}
	for _, sub := range <-s.subs {
		got[sub["type"].(string)] = true
	}
	for _, want := range []string{"pane.agent_status_changed", "pane.updated", "pane.agent_detected", "pane.exited", "pane.closed", "pane.moved"} {
		if !got[want] {
			t.Errorf("not subscribed to %s", want)
		}
	}
}

func TestWatchFiltersOtherPanesAndNamesTheAgent(t *testing.T) {
	s := newFakeServer(t)
	w := watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder")
	s.lines <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p9","agent_status":"working"}}`
	s.lines <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"working"}}`
	ev, ok, err := w.Next(2 * time.Second)
	if err != nil || !ok || ev.PaneID != "w1:p1" || ev.Name != "builder" || ev.Status != "working" {
		t.Fatalf("got %+v ok=%v err=%v", ev, ok, err)
	}
}

func TestWatchFollowsAMovedPane(t *testing.T) {
	s := newFakeServer(t)
	w := watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder")
	s.lines <- `{"event":"pane_moved","data":{"pane":{"pane_id":"w2:p4"},"previous_pane_id":"w1:p1"}}`
	s.lines <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w2:p4","agent_status":"working"}}`
	ev, ok, err := w.Next(2 * time.Second)
	if err != nil || !ok || ev.Kind != "pane_agent_status_changed" || ev.Name != "builder" {
		t.Fatalf("got %+v ok=%v err=%v, want the moved pane's status", ev, ok, err)
	}
}

func TestNextTimesOutWithoutAnEvent(t *testing.T) {
	s := newFakeServer(t)
	w := watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder")
	start := time.Now()
	if _, ok, err := w.Next(150 * time.Millisecond); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want a timeout", ok, err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Error("returned before the timeout")
	}
}

func TestNextReturnsErrorWhenTheSocketCloses(t *testing.T) {
	s := newFakeServer(t)
	w := watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder")
	s.lines <- "CLOSE"
	start := time.Now()
	if _, _, err := w.Next(5 * time.Second); err == nil {
		t.Fatal("no error after the server closed")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("waited for the deadline on a dead connection")
	}
}

func TestWatchDropsOldestAndSetsLostWhenFull(t *testing.T) {
	s := newFakeServer(t)
	w := watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder") // buffer 8
	for i := 0; i < 20; i++ {
		s.lines <- fmt.Sprintf(`{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"s%d"}}`, i)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !w.Lost() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !w.Lost() {
		t.Fatal("a full buffer did not set Lost")
	}
	last := ""
	for {
		ev, ok, err := w.Next(300 * time.Millisecond)
		if err != nil || !ok {
			break
		}
		last = ev.Status
	}
	if last != "s19" {
		t.Errorf("newest event after overflow = %q, want s19", last)
	}
}

func TestWatchKeepsEventsForAnAgentAlreadySnapshotted(t *testing.T) {
	s := newFakeServer(t)
	reads := map[string]int{}
	get := func(n string) (*Agent, error) {
		reads[n]++
		// The second read is the snapshot; the first only finds the pane.
		if n == "second" && reads[n] == 2 {
			s.lines <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"working"}}`
			time.Sleep(200 * time.Millisecond) // let the reader see it before this get returns
		}
		if n == "second" {
			return agentAt(n, "w1:p2", "idle"), nil
		}
		return agentAt(n, "w1:p1", "idle"), nil
	}
	w, err := watchOn(s.dial, get, 8, "first", "second")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	ev, ok, err := w.Next(2 * time.Second)
	if err != nil || !ok || ev.Name != "first" || ev.Status != "working" {
		t.Fatalf("got %+v ok=%v err=%v, want first's status change", ev, ok, err)
	}
}

func TestNextKeepsReturningTheStreamError(t *testing.T) {
	s := newFakeServer(t)
	w := watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle")}, "builder")
	s.lines <- "CLOSE"
	for i := 0; i < 2; i++ {
		start := time.Now()
		if _, _, err := w.Next(5 * time.Second); err == nil {
			t.Fatalf("call %d: no error after the server closed", i)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("call %d waited on a dead connection", i)
		}
	}
	if _, _, err := w.Next(0); err == nil {
		t.Error("Next(0) lost the error")
	}
}

func TestCloseEndsTheReader(t *testing.T) {
	s := newFakeServer(t)
	w, err := watchOn(s.dial, func(n string) (*Agent, error) { return agentAt(n, "w1:p1", "idle"), nil }, 8, "builder")
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the reader did not return after Close")
	}
}

func TestSocketPathPrefersTheEnvironment(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/x/herdr.sock")
	if p, err := SocketPath(); err != nil || p != "/tmp/x/herdr.sock" {
		t.Fatalf("p=%q err=%v", p, err)
	}
}

func TestSocketPathReadsStatusServerWhenUnset(t *testing.T) {
	dir := t.TempDir()
	stub := "#!/bin/sh\n[ \"$1 $2\" = 'status server' ] && printf 'status: running\\nsocket: /tmp/s/herdr.sock\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HERDR_SOCKET_PATH", "")
	if p, err := SocketPath(); err != nil || p != "/tmp/s/herdr.sock" {
		t.Fatalf("p=%q err=%v", p, err)
	}
}
