package main

import (
	"context"
	"testing"

	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

// Herdr's blocked is an agent at an approval or question dialog. It is still a
// turn in progress, so it stays running, and it says it is blocked.
func TestHerdrObserveReportsABlockedPane(t *testing.T) {
	for status, want := range map[string]runner.Observation{
		"blocked": {State: runner.ObsRunning, Blocked: true},
		"working": {State: runner.ObsRunning},
		"idle":    {State: runner.ObsCompleted},
	} {
		st := status
		tr := newHerdrTransport(transportSpec{Builder: "builder"}, herdrDeps{get: func(string) (*herdr.Agent, error) {
			return &herdr.Agent{Status: &st}, nil
		}})
		obs, err := tr.Observe(context.Background(), "e1")
		if err != nil || obs.State != want.State || obs.Blocked != want.Blocked {
			t.Errorf("%s: %+v %v", status, obs, err)
		}
	}
}
