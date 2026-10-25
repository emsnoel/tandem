package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A command that wrote the run's state leaves the bridge a nudge, so the
// bridge ticks now instead of at its backstop. One that wrote nothing leaves
// none (a refused `state gc --apply` must change nothing), and neither does
// a read-only open: the bridge's own watch reads the store, and must not
// wake itself.
func TestCloseNudgesOnlyAfterAWrite(t *testing.T) {
	root := t.TempDir()
	nudge := filepath.Join(root, NudgeFile)
	ctx := context.Background()

	// The first open creates the store; whatever it left is not under test.
	s, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Remove(nudge)

	s, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nudge); !os.IsNotExist(err) {
		t.Fatalf("a writable store that wrote nothing left a nudge (stat err %v)", err)
	}

	s, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TEMP TABLE w(x); INSERT INTO w VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nudge); err != nil {
		t.Fatalf("a store that wrote left no nudge: %v", err)
	}

	if err := os.Remove(nudge); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nudge); !os.IsNotExist(err) {
		t.Fatalf("a read-only store left a nudge (stat err %v)", err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".nudge.*")); len(left) != 0 {
		t.Fatalf("temp nudge files left behind: %v", left)
	}
}
