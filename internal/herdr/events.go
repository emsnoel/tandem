package herdr

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one pane event as tandem reads it. Herdr spells status changes
// "pane.agent_status_changed" and other kinds with underscores; Kind is always
// the underscored form. Name is the watched agent the event's pane belongs to.
type Event struct {
	Kind       string
	Name       string
	PaneID     string
	Status     string
	Session    string
	PrevPaneID string
	Released   bool
}

// Gone reports whether the agent left the pane: its CLI quit (a release; a
// quitting agent does not send pane_exited), the shell exited, or the pane
// closed.
func (e Event) Gone() bool {
	return e.Released || e.Kind == "pane_exited" || e.Kind == "pane_closed"
}

type rawEvent struct {
	Event string `json:"event"`
	Data  struct {
		PaneID      string `json:"pane_id"`
		AgentStatus string `json:"agent_status"`
		Released    bool   `json:"released"`
		PrevPaneID  string `json:"previous_pane_id"`
		Pane        *struct {
			PaneID       string        `json:"pane_id"`
			AgentStatus  string        `json:"agent_status"`
			AgentSession *AgentSession `json:"agent_session"`
		} `json:"pane"`
	} `json:"data"`
}

// decodeEvent reads one line of the event stream. ok is false for a kind
// tandem does not use, so a newer Herdr that adds kinds does not break it.
func decodeEvent(line []byte) (Event, bool, error) {
	var r rawEvent
	if err := json.Unmarshal(line, &r); err != nil {
		return Event{}, false, &Error{Code: "bad_event", Message: err.Error()}
	}
	ev := Event{Kind: strings.ReplaceAll(r.Event, ".", "_"), PaneID: r.Data.PaneID, Status: r.Data.AgentStatus, Released: r.Data.Released, PrevPaneID: r.Data.PrevPaneID}
	if p := r.Data.Pane; p != nil {
		ev.PaneID, ev.Status = p.PaneID, p.AgentStatus
		if p.AgentSession != nil {
			ev.Session = p.AgentSession.Value
		}
	}
	switch ev.Kind {
	case "pane_agent_status_changed", "pane_updated", "pane_agent_detected", "pane_exited", "pane_closed", "pane_moved":
	default:
		return Event{}, false, nil
	}
	if ev.PaneID == "" {
		return Event{}, false, &Error{Code: "bad_event", Message: ev.Kind + " names no pane"}
	}
	return ev, true, nil
}

// SocketPath is the socket the herdr CLI talks to: HERDR_SOCKET_PATH when set,
// otherwise the path `herdr status server` reports, which also covers a named
// session's socket.
func SocketPath() (string, error) {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p, nil
	}
	b, err := Raw("status", "server")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(l, "socket: "); ok {
			return strings.TrimSpace(p), nil
		}
	}
	return "", &Error{Code: "unreachable", Message: "herdr status server names no socket", Unreachable: true}
}

// AgentWatch follows the panes of named agents. A reader goroutine drains the
// socket the whole time, so Herdr never waits on tandem.
type AgentWatch struct {
	conn   net.Conn
	snaps  map[string]*Agent
	mu     sync.Mutex
	panes  map[string]string // pane id -> agent name
	events chan Event
	done   chan struct{} // closed when the reader returns; err is set before
	err    error
	lost   atomic.Bool
	once   sync.Once
}

func (w *AgentWatch) read(r *bufio.Reader) {
	defer close(w.done)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			w.err = &Error{Code: "unreachable", Message: "the event stream ended: " + err.Error(), Unreachable: true}
			return
		}
		ev, ok, err := decodeEvent(line)
		if err != nil {
			w.err = err
			return
		}
		if !ok {
			continue
		}
		w.mu.Lock()
		if ev.Kind == "pane_moved" {
			if n, ok := w.panes[ev.PrevPaneID]; ok {
				delete(w.panes, ev.PrevPaneID)
				w.panes[ev.PaneID] = n
			}
			w.mu.Unlock()
			continue
		}
		name, mine := w.panes[ev.PaneID]
		w.mu.Unlock()
		if !mine {
			continue
		}
		ev.Name = name
		select {
		case w.events <- ev:
		default:
			// Full: drop the oldest so the newest state is never the one lost.
			select {
			case <-w.events:
			default:
			}
			w.events <- ev
			w.lost.Store(true)
		}
	}
}

// Snapshot is the agent as `agent get` showed it right after the subscription.
func (w *AgentWatch) Snapshot(name string) *Agent { return w.snaps[name] }

// Next returns the next event for a watched agent. ok is false when timeout
// passed with none. An error means the stream ended or sent something tandem
// cannot read; it is returned by every call from then on, after any events
// already buffered. The caller decides from a closing read.
func (w *AgentWatch) Next(timeout time.Duration) (Event, bool, error) {
	if ev, ok, err := w.poll(); ok || err != nil || timeout <= 0 {
		return ev, ok, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case ev := <-w.events:
		return ev, true, nil
	case <-w.done:
		return w.poll()
	case <-t.C:
		return Event{}, false, nil
	}
}

// poll returns a buffered event if there is one, else the sticky stream error.
func (w *AgentWatch) poll() (Event, bool, error) {
	select {
	case ev := <-w.events:
		return ev, true, nil
	default:
	}
	select {
	case <-w.done:
		return Event{}, false, w.err
	default:
		return Event{}, false, nil
	}
}

// Lost reports whether an event was dropped because the caller fell behind.
func (w *AgentWatch) Lost() bool { return w.lost.Load() }

// Close ends the subscription and its reader.
func (w *AgentWatch) Close() error {
	var err error
	w.once.Do(func() { err = w.conn.Close() })
	return err
}
