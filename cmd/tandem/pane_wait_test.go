package main

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

func ptr(n int) *int { return &n }

func TestShellReady(t *testing.T) {
	if !shellReady(processInfo{Shell: ptr(1), Foreground: ptr(1)}) || shellReady(processInfo{Shell: ptr(1), Foreground: ptr(4242)}) {
		t.Fatal("shellReady wrong")
	}
}

func TestWaitShellWaitsForTheShellAndTimesOut(t *testing.T) {
	clock := time.Unix(0, 0)
	now, sleep := func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) }
	reads := 0
	ready := func() (processInfo, error) {
		reads++
		if reads < 3 {
			return processInfo{}, errors.New("no info yet")
		}
		return processInfo{Shell: ptr(1), Foreground: ptr(1)}, nil
	}
	if ok, _ := waitShell(ready, time.Minute, now, sleep); !ok || reads != 3 {
		t.Fatalf("reads=%d, want ready on the third", reads)
	}
	busy := func() (processInfo, error) { return processInfo{Shell: ptr(1), Foreground: ptr(9)}, nil }
	if ok, err := waitShell(busy, time.Second, now, sleep); ok || err != nil {
		t.Fatal("a busy pane was reported ready")
	}
}

func TestWaitShellReturnsTheLastReadError(t *testing.T) {
	clock := time.Unix(0, 0)
	down := &herdr.Error{Code: "unreachable", Message: "no socket", Unreachable: true}
	ok, err := waitShell(func() (processInfo, error) { return processInfo{}, down }, time.Second,
		func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) })
	var he *herdr.Error
	if ok || !errors.As(err, &he) || !he.Unreachable || herdr.ExitCodeFor(err) != 3 {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
