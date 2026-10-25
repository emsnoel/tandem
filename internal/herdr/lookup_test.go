package herdr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCLI puts a herdr on PATH that runs body.
func fakeCLI(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A herdr that never answers is a timeout, not an unreachable socket: the
// caller fails closed on one and may fall back on the other.
func TestListWithinTimesOut(t *testing.T) {
	fakeCLI(t, "sleep 30")
	start := time.Now()
	_, err := ListWithin(200 * time.Millisecond)
	if !IsTimeout(err) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if SocketUnreachable(err) {
		t.Fatal("a timeout was classed as an unreachable socket")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("ListWithin took %s; the hung herdr was not killed", took)
	}
}

func TestListWithinReadsTheList(t *testing.T) {
	fakeCLI(t, `printf '{"result":{"agents":[{"pane_id":"w1:p1","name":"reviewer","agent_status":"idle"}]}}\n'`)
	agents, err := ListWithin(5 * time.Second)
	if err != nil || len(agents) != 1 || *agents[0].Name != "reviewer" {
		t.Fatalf("agents %+v, err %v", agents, err)
	}
}

// Only a socket that cannot be reached at all (connect refused, permission
// denied, socket missing, no server) counts as unreachable here. A herdr that
// answers with an error, or prints something unknown, does not.
func TestSocketUnreachableIsNarrow(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       bool
	}{
		{"seatbelt denial", `echo 'Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }' >&2; exit 1`, true},
		{"connection refused", `echo 'Error: Connection refused (os error 61)' >&2; exit 1`, true},
		{"socket missing", `echo 'Error: No such file or directory (os error 2)' >&2; exit 1`, true},
		{"no server", `echo '{"error":{"code":"server_not_running","message":"no herdr server"}}' >&2; exit 1`, true},
		{"herdr declines", `echo '{"error":{"code":"internal","message":"boom"}}' >&2; exit 1`, false},
		{"unknown failure", `echo 'thread main panicked' >&2; exit 101`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeCLI(t, c.body)
			_, err := ListWithin(5 * time.Second)
			if err == nil {
				t.Fatal("no error")
			}
			if got := SocketUnreachable(err); got != c.want {
				t.Fatalf("SocketUnreachable(%v) = %v, want %v", err, got, c.want)
			}
			if IsTimeout(err) {
				t.Fatal("classed as a timeout")
			}
		})
	}
}

func TestHerdrNotOnPathIsNotASocketFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ListWithin(time.Second)
	if err == nil || SocketUnreachable(err) {
		t.Fatalf("err %v: a missing herdr CLI is not a sandboxed socket", err)
	}
}
