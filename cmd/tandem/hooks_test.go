package main

import "testing"

func TestHooksClassifyAlwaysExitsZero(t *testing.T) {
	if got := cmdHooks([]string{"classify", "--codex-home", t.TempDir()}); got != 0 {
		t.Fatalf("hooks classify must always exit 0 (fails open), got %d", got)
	}
}
