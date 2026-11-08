package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the operator's own switches out of the tests.
func TestMain(m *testing.M) {
	os.Unsetenv("TYPESAFE_API_KEY")
	os.Unsetenv("HERDR_DECISION")
	// No test may reach the Herdr session it runs in. A socket path that does not
	// exist makes any call a test forgets to fake fail as unreachable.
	os.Setenv("HERDR_SOCKET_PATH", filepath.Join(os.TempDir(), "tandem-test-no-herdr", "herdr.sock"))
	os.Exit(m.Run())
}
