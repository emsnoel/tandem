package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the operator's own switches out of the tests. With
// HERDR_JUDGE=1 and a key exported, launch-spec probes the network through a
// real codex; tests that want that behaviour set both themselves, against a
// stub, and nothing else should inherit it by accident.
func TestMain(m *testing.M) {
	os.Unsetenv("HERDR_JUDGE")
	os.Unsetenv("TYPESAFE_API_KEY")
	// A machine that enables skipping must not change what these tests see;
	// the ones that exercise it set it themselves.
	os.Unsetenv("HERDR_TRIAGE_SKIP")
	os.Unsetenv("HERDR_DECISION")
	// No test may reach the Herdr session it runs in. A socket path that does not
	// exist makes any call a test forgets to fake fail as unreachable.
	os.Setenv("HERDR_SOCKET_PATH", filepath.Join(os.TempDir(), "tandem-test-no-herdr", "herdr.sock"))
	os.Exit(m.Run())
}
