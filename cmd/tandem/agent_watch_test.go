package main

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/mailwatch"
)

// scriptWatch is an eventWatch that plays back events in order. A zero Event
// with ok false is a timeout; err ends the stream.
type scriptWatch struct {
	snaps  map[string]*herdr.Agent
	steps  []scriptStep
	clock  *time.Time
	closed bool
}

type scriptStep struct {
	after time.Duration
	ev    herdr.Event
	err   error
}

func (s *scriptWatch) Snapshot(n string) *herdr.Agent { return s.snaps[n] }
func (s *scriptWatch) Lost() bool                     { return false }
func (s *scriptWatch) Close() error                   { s.closed = true; return nil }
func (s *scriptWatch) Next(timeout time.Duration) (herdr.Event, bool, error) {
	if len(s.steps) == 0 || s.steps[0].after > timeout {
		if s.clock != nil {
			*s.clock = s.clock.Add(timeout)
		}
		if len(s.steps) > 0 {
			s.steps[0].after -= timeout
		}
		return herdr.Event{}, false, nil
	}
	st := s.steps[0]
	s.steps = s.steps[1:]
	if s.clock != nil {
		*s.clock = s.clock.Add(st.after)
	}
	if st.err != nil {
		return herdr.Event{}, false, st.err
	}
	return st.ev, true, nil
}

func TestAgentWatchReportsAStatusChange(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{after: time.Second, ev: herdr.Event{Kind: "pane_agent_status_changed", Name: "builder", Status: "idle"}}}}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, 5*time.Second, &out); code != exitcode.OK || out.String() != "changed\tbuilder\tidle\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchReportsAGoneAgent(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{ev: herdr.Event{Kind: "pane_agent_detected", Name: "reviewer", Released: true}}}}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, 5*time.Second, &out); code != exitcode.AgentExited || out.String() != "agent_exited\treviewer\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchTimesOutQuietly(t *testing.T) {
	var out bytes.Buffer
	if code := runAgentWatch(&scriptWatch{}, nil, time.Second, &out); code != exitcode.OK || out.String() != "timeout\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchIgnoresUpdatesWithoutAStatusChange(t *testing.T) {
	w := &scriptWatch{
		snaps: map[string]*herdr.Agent{},
		steps: []scriptStep{{ev: herdr.Event{Kind: "pane_updated", Name: "builder", Status: ""}}},
	}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, time.Second, &out); code != exitcode.OK || out.String() != "timeout\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchReportsADroppedStreamAsAnError(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{err: &herdr.Error{Code: "socket_closed", Message: "stream dropped", Unreachable: true}}}}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, 5*time.Second, &out); code != exitcode.Unreachable || out.String() != "" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchReportsAnUndecodableStreamAsInternal(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{err: errors.New("bad line")}}}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, 5*time.Second, &out); code != exitcode.Internal || out.String() != "" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

// After a pane moves, its status subscription names the old pane id, so a
// status change arrives only in pane_updated. One whose status differs from
// the last one seen counts; one that repeats it does not (the same rule as
// runReset's).
func TestAgentWatchCountsAPaneUpdatedWhoseStatusChanged(t *testing.T) {
	idle := "idle"
	snaps := map[string]*herdr.Agent{"builder": {PaneID: "w1:p1", Status: &idle}}
	w := &scriptWatch{snaps: snaps, steps: []scriptStep{
		{ev: herdr.Event{Kind: "pane_moved", Name: "builder", PaneID: "w2:p4"}},
		{ev: herdr.Event{Kind: "pane_updated", Name: "builder", Status: "idle"}},
		{ev: herdr.Event{Kind: "pane_updated", Name: "builder", Status: "working"}},
	}}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, 5*time.Second, &out); code != exitcode.OK || out.String() != "changed\tbuilder\tworking\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchIgnoresAPaneUpdatedThatRepeatsTheStatus(t *testing.T) {
	idle := "idle"
	w := &scriptWatch{snaps: map[string]*herdr.Agent{"builder": {PaneID: "w1:p1", Status: &idle}}, steps: []scriptStep{
		{ev: herdr.Event{Kind: "pane_updated", Name: "builder", Status: "idle", Session: "s2"}},
	}}
	var out bytes.Buffer
	if code := runAgentWatch(w, nil, time.Second, &out); code != exitcode.OK || out.String() != "timeout\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

// fakeMail is a mailbox source the test feeds by hand.
type fakeMail struct {
	wakes  chan mailwatch.Wake
	errs   chan error
	closed bool
}

func newFakeMail() *fakeMail {
	return &fakeMail{wakes: make(chan mailwatch.Wake, 1), errs: make(chan error, 1)}
}
func (f *fakeMail) Wakes() <-chan mailwatch.Wake { return f.wakes }
func (f *fakeMail) Errors() <-chan error         { return f.errs }
func (f *fakeMail) Close() error                 { f.closed = true; return nil }

// blockWatch is a Herdr watch with nothing to say: Next blocks until Close or
// its timeout, as a live watch on a quiet run does.
type blockWatch struct {
	stop   chan struct{}
	once   sync.Once
	closed bool
}

func newBlockWatch() *blockWatch                   { return &blockWatch{stop: make(chan struct{})} }
func (b *blockWatch) Snapshot(string) *herdr.Agent { return nil }
func (b *blockWatch) Lost() bool                   { return false }
func (b *blockWatch) Close() error {
	b.once.Do(func() { b.closed = true; close(b.stop) })
	return nil
}
func (b *blockWatch) Next(timeout time.Duration) (herdr.Event, bool, error) {
	select {
	case <-b.stop:
		return herdr.Event{}, false, errors.New("watch closed")
	case <-time.After(timeout):
		return herdr.Event{}, false, nil
	}
}

func TestAgentWatchReportsAQueuedMessageOverAQuietRun(t *testing.T) {
	w, m := newBlockWatch(), newFakeMail()
	m.wakes <- mailwatch.Wake{Kind: "queued", Path: "/r/q/a.json"}
	var out bytes.Buffer
	if code := runAgentWatch(w, m, 5*time.Second, &out); code != exitcode.OK || out.String() != "queued\t/r/q/a.json\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
	if !w.closed || !m.closed {
		t.Fatalf("closed: herdr %v mailbox %v", w.closed, m.closed)
	}
}

func TestAgentWatchReportsAStatusChangeOverAQuietMailbox(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{after: time.Second, ev: herdr.Event{Kind: "pane_agent_status_changed", Name: "builder", Status: "idle"}}}}
	m := newFakeMail()
	var out bytes.Buffer
	if code := runAgentWatch(w, m, 5*time.Second, &out); code != exitcode.OK || out.String() != "changed\tbuilder\tidle\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
	if !m.closed {
		t.Fatal("the mailbox watch was left open")
	}
}

// The bridge scans and ticks after a mailbox error as after any wake, so the
// error is a wake, not a failure.
func TestAgentWatchReportsAMailboxErrorAsAWake(t *testing.T) {
	m := newFakeMail()
	m.errs <- errors.New("event queue\noverflow")
	var out bytes.Buffer
	if code := runAgentWatch(nil, m, 5*time.Second, &out); code != exitcode.OK || out.String() != "mailbox_error\tevent queue overflow\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchOnTheMailboxAloneTimesOut(t *testing.T) {
	var out bytes.Buffer
	if code := runAgentWatch(nil, newFakeMail(), 50*time.Millisecond, &out); code != exitcode.OK || out.String() != "timeout\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

// scriptWatch reports its timeout at once, on a fake clock. With a mailbox
// armed, that must not end the wait: the mailbox keeps its own deadline.
func TestAgentWatchKeepsWaitingOnTheMailboxAfterHerdrTimesOut(t *testing.T) {
	m := newFakeMail()
	go func() {
		time.Sleep(50 * time.Millisecond)
		m.wakes <- mailwatch.Wake{Kind: "nudge", Path: "/s/nudge"}
	}()
	var out bytes.Buffer
	if code := runAgentWatch(&scriptWatch{}, m, 5*time.Second, &out); code != exitcode.OK || out.String() != "nudge\t/s/nudge\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestAgentWatchStillReportsABrokenStreamWithAMailbox(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{err: &herdr.Error{Code: "socket_closed", Message: "stream dropped", Unreachable: true}}}}
	var out bytes.Buffer
	if code := runAgentWatch(w, newFakeMail(), 5*time.Second, &out); code != exitcode.Unreachable || out.String() != "" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

// When the mailbox wins, closing the Herdr watch makes its Next fail. That
// error is not the answer and must not reach stderr: herdrWait returns it,
// and only the result that decides is printed.
func TestHerdrWaitLeavesItsErrorToTheCaller(t *testing.T) {
	w := &scriptWatch{steps: []scriptStep{{err: &herdr.Error{Code: "socket_closed", Message: "stream dropped", Unreachable: true}}}}
	r := herdrWait(w, time.Second)
	if !r.decided || r.code != exitcode.Unreachable || r.errText == "" {
		t.Fatalf("result %+v", r)
	}
}
