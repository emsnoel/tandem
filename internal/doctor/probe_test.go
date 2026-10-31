package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A CLI whose --version leaves a child holding stdout open (an npm shim that
// starts the native binary, say) must not hold doctor past its timeout.
func TestCLIVersionIsBoundedWhenAChildHoldsOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slowcli")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 8 &\nsleep 8\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (RealProbe{}).CLIVersion(ctx, script); err == nil {
		t.Fatal("want an error from a CLI that never answered")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s, want under 3s", d)
	}
}
