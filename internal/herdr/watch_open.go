package herdr

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Opening a watch. Herdr requires pane_id on a pane.agent_status_changed
// subscription (invalid_request without one, pane_not_found for an unknown
// pane), so a watch learns each agent's pane first. Other kinds are requested
// unfiltered and the reader drops events for unwatched panes.
//
// The snapshot is taken after the subscription is acknowledged, so nothing
// after it is missed. A pane that moved before the snapshot is not covered, so
// the open starts over once.

// unfiltered are the kinds every watch asks for on every pane.
var unfiltered = []string{"pane.updated", "pane.agent_detected", "pane.exited", "pane.closed", "pane.moved"}

// subscriptionsFor is the subscription list for watched panes: one status
// subscription per pane, then the unfiltered kinds.
func subscriptionsFor(panes []string) []map[string]string {
	subs := make([]map[string]string, 0, len(panes)+len(unfiltered))
	for _, p := range panes {
		subs = append(subs, map[string]string{"type": "pane.agent_status_changed", "pane_id": p})
	}
	for _, s := range unfiltered {
		subs = append(subs, map[string]string{"type": s})
	}
	return subs
}

// IsGone reports whether err is Herdr saying the agent, or the pane it was
// in, no longer exists: agent_not_found from a read, or pane_not_found from a
// subscription for a pane the agent left.
func IsGone(err error) bool {
	var he *Error
	return errors.As(err, &he) && (he.Code == "agent_not_found" || he.Code == "pane_not_found")
}

// Watch resolves each agent's pane, subscribes, then snapshots each agent
// with `agent get`, then returns. An event for an agent that arrives before
// that agent's own snapshot is dropped; once its snapshot is taken, its
// events are kept.
func Watch(names ...string) (*AgentWatch, error) {
	path, err := SocketPath()
	if err != nil {
		return nil, err
	}
	dial := func() (net.Conn, error) {
		c, err := net.Dial("unix", path)
		if err != nil {
			return nil, &Error{Code: "unreachable", Message: err.Error(), Unreachable: true}
		}
		return c, nil
	}
	return watchOn(dial, Get, 256, names...)
}

// watchOn opens the watch, and once more when the first open found a pane
// gone or moved under it. The second open's answer stands: a pane gone again
// is pane_not_found (or the agent's own agent_not_found, from its read), a
// pane moved again is unreachable.
func watchOn(dial func() (net.Conn, error), get func(string) (*Agent, error), buffer int, names ...string) (*AgentWatch, error) {
	w, retry, err := openWatch(dial, get, buffer, names)
	if retry {
		w, _, err = openWatch(dial, get, buffer, names)
	}
	return w, err
}

// openWatch is one attempt. retry is true when the pane of an agent changed
// between its first read and the subscription or the snapshot.
func openWatch(dial func() (net.Conn, error), get func(string) (*Agent, error), buffer int, names []string) (w *AgentWatch, retry bool, err error) {
	paneOf := map[string]string{}
	var panes []string
	for _, n := range names {
		a, err := get(n)
		if err != nil {
			return nil, false, agentGone(err, n)
		}
		if _, dup := indexOf(panes, a.PaneID); !dup {
			panes = append(panes, a.PaneID)
		}
		paneOf[n] = a.PaneID
	}
	conn, err := dial()
	if err != nil {
		return nil, false, err
	}
	req, _ := json.Marshal(map[string]any{"id": "tandem-watch", "method": "events.subscribe", "params": map[string]any{"subscriptions": subscriptionsFor(panes)}})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		conn.Close()
		return nil, false, &Error{Code: "unreachable", Message: err.Error(), Unreachable: true}
	}
	r := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack, err := r.ReadBytes('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, false, &Error{Code: "unreachable", Message: err.Error(), Unreachable: true}
	}
	if e := decodeError(ack); e != nil {
		conn.Close()
		if e.Code == "pane_not_found" {
			return nil, true, paneGone(e, names, paneOf)
		}
		return nil, false, e
	}
	w = &AgentWatch{conn: conn, snaps: map[string]*Agent{}, panes: map[string]string{}, events: make(chan Event, buffer), done: make(chan struct{})}
	go w.read(r)
	for _, n := range names {
		a, err := get(n)
		if err != nil {
			w.Close()
			return nil, false, agentGone(err, n)
		}
		if a.PaneID != paneOf[n] {
			w.Close()
			return nil, true, &Error{Code: "unreachable", Message: fmt.Sprintf("the pane of %s changed from %s to %s while the event watch opened, on two tries in a row", n, paneOf[n], a.PaneID), Unreachable: true}
		}
		w.snaps[n] = a
		w.mu.Lock()
		w.panes[a.PaneID] = n
		w.mu.Unlock()
	}
	return w, false, nil
}

// paneGoneRe matches Herdr's pane_not_found message: "pane w8:pNOPE not found".
var paneGoneRe = regexp.MustCompile(`^pane (\S+) not found$`)

// paneGone names the agent whose pane Herdr no longer has. It parses the pane
// id from the message and takes the first agent, in names order, resolved to
// exactly that pane; a substring match would let "w1:p12" name the agent on
// "w1:p1". The name is also carried on the error's Agent field.
func paneGone(e *Error, names []string, paneOf map[string]string) *Error {
	if m := paneGoneRe.FindStringSubmatch(e.Message); m != nil {
		for _, n := range names {
			if paneOf[n] == m[1] {
				return &Error{Code: e.Code, Message: fmt.Sprintf("%s left its pane %s: %s", n, paneOf[n], e.Message), Agent: n}
			}
		}
	}
	return &Error{Code: e.Code, Message: fmt.Sprintf("a watched agent (%s) left its pane: %s", strings.Join(names, ", "), e.Message)}
}

// agentGone marks an agent_not_found from reading n as being about n.
func agentGone(err error, n string) error {
	var he *Error
	if errors.As(err, &he) && he.Code == "agent_not_found" {
		c := *he
		c.Agent = n
		return &c
	}
	return err
}

func indexOf(xs []string, x string) (int, bool) {
	for i, v := range xs {
		if v == x {
			return i, true
		}
	}
	return -1, false
}
