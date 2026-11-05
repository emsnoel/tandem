package main

import (
	"context"
	"errors"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

// closingPanes reads a pane for the closing transport. Tests replace it.
var closingPanes = herdr.Get

// closingTransport serves a run tandem can only close: one stored with a kind
// that has no adapter, or with no reviewer. It never types anything. It watches
// the builder's pane, because a stop needs to see the builder settle before it
// frees the writer, and watching a pane takes the agent's name, not an adapter.
//
// It also observes any agent by name, so a verification prompt is judged by the
// planner's pane and not the builder's. It is no runner.PaneReader, because no
// pane review is read from a run waiting to be closed.
type closingTransport struct {
	builder string
	// builderDiscontinued: a person cannot restart the builder through tandem,
	// so a pane that is gone stays gone and counts as stopped. A supported
	// builder in a mixed run can come back, so its gone pane stays an error.
	builderDiscontinued bool
	panes               *herdrTransport
}

func newClosingTransport(ag controller.Agents) closingTransport {
	return closingTransport{
		builder:             ag.BuilderSession,
		builderDiscontinued: agent.FirstUnsupported(ag.BuilderKind) != "",
		panes:               newHerdrTransport(transportSpec{}, herdrDeps{get: closingPanes}),
	}
}

func (closingTransport) Send(context.Context, runner.Delivery) (runner.Receipt, error) {
	return runner.Receipt{}, runner.NotSent(errors.New("this run can only be closed, so tandem types nothing into its panes"))
}

func (t closingTransport) Observe(ctx context.Context, _ string) (runner.Observation, error) {
	obs, err := t.panes.ObserveAgent(ctx, t.builder)
	if t.builderDiscontinued && errors.Is(err, runner.ErrAgentGone) {
		return runner.Observation{State: runner.ObsStopped}, nil
	}
	return obs, err
}

// ObserveAgent reads the named pane. A gone pane stays an error here, because
// only the builder's stop treats it as stopped.
func (t closingTransport) ObserveAgent(ctx context.Context, name string) (runner.Observation, error) {
	return t.panes.ObserveAgent(ctx, name)
}
