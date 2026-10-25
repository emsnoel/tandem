package main

import (
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

const watchStep = 500 * time.Millisecond

// stepWatch replays a read-driven fake as events. Each step advances the
// fake's clock by watchStep, reads the agent once, and reports what changed: a
// status, a session, or the agent gone. pending, when set, is asked first for
// events the fake owes at once, so a turn that starts and ends inside one step
// is still seen.
type stepWatch struct {
	name    string
	snap    *herdr.Agent
	read    func(name string) (*herdr.Agent, error)
	advance func(time.Duration)
	pending func() []herdr.Event
	last    string
	session string
	gone    bool
	queue   []herdr.Event
}

func newStepWatch(name string, read func(string) (*herdr.Agent, error), advance func(time.Duration)) (*stepWatch, error) {
	a, err := read(name)
	if err != nil {
		return nil, err
	}
	return &stepWatch{name: name, snap: a, read: read, advance: advance, last: a.StatusString(), session: a.SessionID()}, nil
}

func (w *stepWatch) Snapshot(string) *herdr.Agent { return w.snap }
func (w *stepWatch) Lost() bool                   { return false }
func (w *stepWatch) Close() error                 { return nil }

func (w *stepWatch) Next(timeout time.Duration) (herdr.Event, bool, error) {
	if w.pending != nil {
		for _, ev := range w.pending() {
			if ev.Status != "" {
				w.last = ev.Status
			}
			w.queue = append(w.queue, ev)
		}
	}
	if len(w.queue) > 0 {
		ev := w.queue[0]
		w.queue = w.queue[1:]
		return ev, true, nil
	}
	for waited := time.Duration(0); waited < timeout; waited += watchStep {
		w.advance(watchStep)
		a, err := w.read(w.name)
		if err != nil {
			if w.gone {
				continue
			}
			w.gone = true
			return herdr.Event{Kind: "pane_agent_detected", Name: w.name, PaneID: "%1", Released: true}, true, nil
		}
		if st := a.StatusString(); st != w.last {
			w.last = st
			return herdr.Event{Kind: "pane_agent_status_changed", Name: w.name, PaneID: "%1", Status: st}, true, nil
		}
		if s := a.SessionID(); s != w.session {
			w.session = s
			return herdr.Event{Kind: "pane_updated", Name: w.name, PaneID: "%1", Status: a.StatusString(), Session: s}, true, nil
		}
	}
	return herdr.Event{}, false, nil
}
