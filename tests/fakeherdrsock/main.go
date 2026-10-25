// fakeherdrsock serves Herdr's event stream for the shell suite. Every 50 ms it
// runs `herdr agent list` (the test's fake CLI) and sends the events the real
// server would for what changed. A pane that leaves the list is a release.
//
// Subscriptions follow Herdr 0.9.3's schema: a status subscription without
// pane_id gets invalid_request, one for an unknown pane gets pane_not_found,
// and clients get only the kinds and panes they asked for.
//
// A pane whose state_change_seq moved while its status did not ran a turn too
// short to poll, so this sends working and then the status again.
//
// It exits when its lifeline directory (os.Args[2]) is removed.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type agent struct {
	PaneID  string `json:"pane_id"`
	Status  string `json:"agent_status"`
	Seq     int64  `json:"state_change_seq"`
	Session *struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

func list() map[string]agent {
	out, err := exec.Command("herdr", "agent", "list").Output()
	if err != nil {
		return nil
	}
	var env struct {
		Result struct {
			Agents []agent `json:"agents"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &env) != nil {
		return nil
	}
	m := map[string]agent{}
	for _, a := range env.Result.Agents {
		m[a.PaneID] = a
	}
	return m
}

func sess(a agent) string {
	if a.Session == nil {
		return ""
	}
	return a.Session.Value
}

// client is one subscriber: the kinds it asked for, and the panes whose
// status changes it asked for.
type client struct {
	kinds  map[string]bool
	status map[string]bool
}

type subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id"`
}

// subscribe checks a request the way Herdr 0.9.3 does and returns the client,
// or Herdr's error code and message.
func subscribe(subs []subscription) (*client, string, string) {
	c := &client{kinds: map[string]bool{}, status: map[string]bool{}}
	var panes map[string]agent
	for _, s := range subs {
		if s.Type != "pane.agent_status_changed" {
			c.kinds[s.Type] = true
			continue
		}
		if s.PaneID == "" {
			return nil, "invalid_request", "invalid request: missing field `pane_id`"
		}
		if panes == nil {
			panes = list()
		}
		if _, ok := panes[s.PaneID]; !ok {
			return nil, "pane_not_found", "pane " + s.PaneID + " not found"
		}
		c.status[s.PaneID] = true
	}
	return c, "", ""
}

func main() {
	path := os.Args[1]
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var mu sync.Mutex
	clients := map[net.Conn]*client{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					c.Close()
					return
				}
				var req struct {
					ID     string `json:"id"`
					Params struct {
						Subscriptions []subscription `json:"subscriptions"`
					} `json:"params"`
				}
				_ = json.Unmarshal(line, &req)
				cl, code, msg := subscribe(req.Params.Subscriptions)
				if cl == nil {
					b, _ := json.Marshal(map[string]any{"id": req.ID, "error": map[string]string{"code": code, "message": msg}})
					fmt.Fprintf(c, "%s\n", b)
					c.Close()
					return
				}
				// Ack and register under one lock: send holds it too, so no
				// event can fall between the reply and the registration.
				mu.Lock()
				fmt.Fprintf(c, `{"id":%q,"result":{"type":"subscription_started"}}`+"\n", req.ID)
				clients[c] = cl
				mu.Unlock()
			}()
		}
	}()
	// send writes line to every client subscribed to kind, and for a status
	// change only to those subscribed to its pane.
	send := func(kind, pane, line string) {
		mu.Lock()
		defer mu.Unlock()
		for c, cl := range clients {
			if kind == "pane.agent_status_changed" && !cl.status[pane] || kind != "pane.agent_status_changed" && !cl.kinds[kind] {
				continue
			}
			if _, err := fmt.Fprintln(c, line); err != nil {
				c.Close()
				delete(clients, c)
			}
		}
	}
	status := func(id, st string) {
		send("pane.agent_status_changed", id, fmt.Sprintf(`{"event":"pane.agent_status_changed","data":{"pane_id":%q,"agent_status":%q}}`, id, st))
	}
	updated := func(id string, a agent) {
		send("pane.updated", id, fmt.Sprintf(`{"event":"pane_updated","data":{"pane":{"pane_id":%q,"agent_status":%q,"agent_session":{"value":%q}}}}`, id, a.Status, sess(a)))
	}
	prev := list()
	for {
		time.Sleep(50 * time.Millisecond)
		if _, err := os.Stat(os.Args[2]); err != nil {
			os.RemoveAll(filepath.Dir(path))
			return
		}
		cur := list()
		if cur == nil {
			continue
		}
		for id, a := range cur {
			p, seen := prev[id]
			if !seen {
				continue
			}
			switch {
			case p.Status != a.Status:
				status(id, a.Status)
			case p.Seq != a.Seq:
				// A turn that started and ended between two polls.
				status(id, "working")
				status(id, a.Status)
			}
			if p.Status != a.Status || sess(p) != sess(a) {
				updated(id, a)
			}
		}
		for id, p := range prev {
			if _, ok := cur[id]; !ok {
				send("pane.agent_detected", id, fmt.Sprintf(`{"event":"pane_agent_detected","data":{"pane_id":%q,"released":true,"final_status":%q}}`, id, p.Status))
			}
		}
		prev = cur
	}
}
