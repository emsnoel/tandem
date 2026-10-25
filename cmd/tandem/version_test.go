package main

import (
	"strings"
	"testing"
)

// stamp sets the two link-time variables for one test and puts them back.
func stamp(t *testing.T, v, hash string) {
	t.Helper()
	oldV, oldH := version, sourceHash
	version, sourceHash = v, hash
	t.Cleanup(func() { version, sourceHash = oldV, oldH })
}

func TestVersionPrintsTagAndSource(t *testing.T) {
	hash := strings.Repeat("0123456789abcdef", 4)
	stamp(t, "v1.2.3", hash)
	got := captureStdout(t, func() int { return cmdVersion(nil) })
	if want := "tandem v1.2.3 (source 0123456789ab)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionDefaultsToDevUnstamped(t *testing.T) {
	stamp(t, "dev", "")
	got := captureStdout(t, func() int { return cmdVersion(nil) })
	if want := "tandem dev (source unstamped)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionHashIsUnchanged(t *testing.T) {
	hash := strings.Repeat("f", 64)
	stamp(t, "v1.2.3", hash)
	got := captureStdout(t, func() int { return cmdVersion([]string{"--hash"}) })
	if got != hash+"\n" {
		t.Fatalf("got %q, want the bare hash", got)
	}
}
