package main

import (
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/controller"
)

// withWindow sets the silence window for one test, as TANDEM_SILENCE_WINDOW_MS
// does for a process.
func withWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := controller.SilenceWindow
	controller.SilenceWindow = d
	t.Cleanup(func() { controller.SilenceWindow = old })
}

// The quiet pauses say how long the pane stayed quiet, in the window this
// tandem was given, so a note never states a time the run did not use.
func TestQuietPauseReasonsGiveTheWindow(t *testing.T) {
	for _, c := range []struct {
		code   string
		window time.Duration
		want   string
	}{
		{"builder_blocked", 10 * time.Minute, "The builder's pane stayed blocked at a dialog for 10 minutes."},
		{"builder_blocked", 3 * time.Second, "The builder's pane stayed blocked at a dialog for 3 seconds."},
	} {
		withWindow(t, c.window)
		_, open := openPaused(t, c.code)
		if got := reasonOf(t, open); got != c.want {
			t.Errorf("%s at %s: reason = %q, want %q", c.code, c.window, got, c.want)
		}
	}
}

func TestWindowText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute:        "10 minutes",
		time.Minute:             "1 minute",
		90 * time.Second:        "90 seconds",
		time.Second:             "1 second",
		1500 * time.Millisecond: "2 seconds",
		100 * time.Millisecond:  "1 second",
	} {
		if got := windowText(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}
