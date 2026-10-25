package herdr

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeServer is a socket that answers events.subscribe the way Herdr 0.9.3
// does and then writes whatever the test sends on lines to the connection
// that subscribed last. It rejects a pane.agent_status_changed subscription
// without pane_id with Herdr's own error, and answers pane_not_found for a
// pane id in missing. It accepts any number of connections, so a watch that
// retries its open can be tested.
type fakeServer struct {
	path  string
	lines chan string
	subs  chan []map[string]any

	mu      sync.Mutex
	missing map[string]bool // pane ids that answer pane_not_found
	opens   int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tev")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &fakeServer{path: filepath.Join(dir, "s.sock"), lines: make(chan string, 64), subs: make(chan []map[string]any, 8), missing: map[string]bool{}}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

// subscribeError is the error Herdr 0.9.3 answers for subs, or "".
func (s *fakeServer) subscribeError(subs []map[string]any) (code, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range subs {
		if sub["type"] != "pane.agent_status_changed" {
			continue
		}
		p, _ := sub["pane_id"].(string)
		if p == "" {
			return "invalid_request", "invalid request: missing field `pane_id`"
		}
		if s.missing[p] {
			return "pane_not_found", "pane " + p + " not found"
		}
	}
	return "", ""
}

func (s *fakeServer) serve(c net.Conn) {
	defer c.Close()
	var req struct {
		ID     string `json:"id"`
		Params struct {
			Subscriptions []map[string]any `json:"subscriptions"`
		} `json:"params"`
	}
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	_ = json.Unmarshal(line, &req)
	s.subs <- req.Params.Subscriptions
	s.mu.Lock()
	s.opens++
	s.mu.Unlock()
	if code, msg := s.subscribeError(req.Params.Subscriptions); code != "" {
		b, _ := json.Marshal(map[string]any{"id": req.ID, "error": map[string]string{"code": code, "message": msg}})
		c.Write(append(b, '\n'))
		return
	}
	c.Write([]byte(`{"id":"` + req.ID + `","result":{"type":"subscription_started"}}` + "\n"))
	// The client closing its end stops this connection taking lines meant for
	// the next one.
	gone := make(chan struct{})
	go func() {
		r.ReadBytes('\n')
		close(gone)
	}()
	for {
		select {
		case <-gone:
			return
		case l := <-s.lines:
			if l == "CLOSE" {
				return
			}
			c.Write([]byte(l + "\n"))
		}
	}
}

func (s *fakeServer) setMissing(pane string, missing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.missing[pane] = missing
}

func (s *fakeServer) openCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens
}

func (s *fakeServer) dial() (net.Conn, error) { return net.Dial("unix", s.path) }
