package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Lines agy 1.5.14 wrote to its process log around a /new (log/cli-*.log,
// pid and ids from a real run). The first line names the process.
const agyLog = `I1121 16:17:18.202874      26 server.go:2652] Starting language server process with pid 33768
I1121 16:17:24.493412    1090 conversation_manager.go:1067] Forwarding user message to conversation 48b1044d-4dd8-44bc-b48d-bdc1ff497ebd (items=1, media=0)
I1121 16:17:37.328797    1179 conversation_manager.go:836] Starting new conversation (agent=false)
I1121 16:17:37.338116    1179 server.go:1269] Created conversation e76daff4-0bf2-45c9-9d1b-41b458ddc8cf (backend=BACKEND_TYPE_UNSPECIFIED)
I1121 16:17:37.338638    1179 server.go:3575] GetConversationDetail: found conversation e76daff4-0bf2-45c9-9d1b-41b458ddc8cf (active=true)
`

func writeAgyLog(t *testing.T, logs map[string]string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range logs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func local(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAgyLogNamesTheConversationCreatedSince(t *testing.T) {
	home := writeAgyLog(t, map[string]string{"cli-20261121_161718.log": agyLog})
	if got := agyLogSession(home, 33768, local(t, "2026-11-21 16:17:30")); got != "e76daff4-0bf2-45c9-9d1b-41b458ddc8cf" {
		t.Errorf("got %q", got)
	}
	// A conversation created before the reset command does not confirm it.
	if got := agyLogSession(home, 33768, local(t, "2026-11-21 16:17:40")); got != "" {
		t.Errorf("a conversation from before the reset confirmed it: %q", got)
	}
}

// Every agy process writes its own log, and the log's first line names the
// pid. Another pane's agy creating a conversation never confirms this one.
func TestAgyLogIsThePanesOwn(t *testing.T) {
	other := `I1121 16:17:01.000000      26 server.go:2652] Starting language server process with pid 4242
I1121 16:17:38.000000    1179 server.go:1269] Created conversation 11111111-2222-3333-4444-555555555555 (backend=BACKEND_TYPE_UNSPECIFIED)
`
	home := writeAgyLog(t, map[string]string{"cli-20261121_161701.log": other, "cli-20261121_161718.log": agyLog})
	since := local(t, "2026-11-21 16:17:30")
	if got := agyLogSession(home, 33768, since); got != "e76daff4-0bf2-45c9-9d1b-41b458ddc8cf" {
		t.Errorf("pid 33768: got %q", got)
	}
	if got := agyLogSession(home, 9999, since); got != "" {
		t.Errorf("a pid with no log confirmed a reset: %q", got)
	}
}

func TestAgyLogIgnoresOtherLines(t *testing.T) {
	body := `I1121 16:17:18.202874      26 server.go:2652] Starting language server process with pid 33768
I1121 16:17:37.338638    1179 server.go:3575] GetConversationDetail: found conversation e76daff4-0bf2-45c9-9d1b-41b458ddc8cf (active=true)
`
	home := writeAgyLog(t, map[string]string{"cli-20261121_161718.log": body})
	if got := agyLogSession(home, 33768, local(t, "2026-11-21 16:17:30")); got != "" {
		t.Errorf("a line that does not create a conversation confirmed a reset: %q", got)
	}
}
