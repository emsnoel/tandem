package main

import (
	"context"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/herdr"
)

// runningProbe is okProbe with a builder already running in w1:p2.
type runningProbe struct{ okProbe }

func (runningProbe) Agents(context.Context) ([]herdr.Agent, error) {
	name := "builder"
	return []herdr.Agent{{PaneID: "w1:p2", Name: &name, Kind: "claude"}}, nil
}

// A re-run to repair a half-built pair: doctor finds the running builder.
func TestDoctorReportsARunningBuilder(t *testing.T) {
	root := healthyRepo(t)
	t.Setenv("HERDR_BUILDER_NAME", "")
	doctorProbe = runningProbe{}
	out, _ := captureStdoutRC(t, func() int { return cmdDoctor([]string{"--cwd", root, "--builder", "claude", "--reviewer", "claude"}) })
	if !strings.Contains(out, "builder already runs in pane w1:p2") {
		t.Fatalf("doctor did not report the running builder:\n%s", out)
	}
}
