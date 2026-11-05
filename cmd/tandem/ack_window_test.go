package main

import (
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/state"
)

func TestAckWindowFromEnv(t *testing.T) {
	def := state.AckWindow
	for env, want := range map[string]time.Duration{"": def, "abc": def, "0": def, "-5": def, "1500": 1500 * time.Millisecond} {
		t.Setenv("TANDEM_ACK_WINDOW_MS", env)
		if got := ackWindowFromEnv(); got != want {
			t.Errorf("TANDEM_ACK_WINDOW_MS=%q: %v, want %v", env, got, want)
		}
	}
}
