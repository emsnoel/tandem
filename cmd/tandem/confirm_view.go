package main

// Whether a pane shows its own conversation's input before anything is typed.
//
// Herdr's status is the pane's, not the conversation's. A planner showing a
// subagent's screen reads idle, and text typed there reaches the subagent.
// Nothing is typed into such a screen: the sender waits as for a dialog, then
// refuses.

import (
	"fmt"
	"io"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// A screen change sends no Herdr event, so the screen is polled with backoff
// from viewPoll up to viewPollMax.
const (
	viewPoll    = 500 * time.Millisecond
	viewPollMax = 4 * time.Second
)

// liveScreen is the pane's screen as Herdr renders it.
func liveScreen(name string) (string, error) {
	b, err := herdr.Raw(readArgs(name, "visible", 0)...)
	return string(b), err
}

// offMain reads name's screen once as kind's. A screen that cannot be read is
// not off: that read failing says nothing about what the pane shows.
func offMain(screen func(string) (string, error), kind, name string) (string, bool) {
	if screen == nil {
		return "", false
	}
	text, err := screen(name)
	if err != nil {
		return "", false
	}
	return agent.OffMainInput(kind, text)
}

// awaitMainInput waits up to within for name's pane to show its main
// conversation's input. why is what it still shows at the deadline, "" once it
// shows the input; waited says the screen was off at first, so whatever was
// read of the pane before is stale.
func awaitMainInput(d herdrDeps, kind, name string, within time.Duration) (why string, waited bool) {
	why, off := offMain(d.screen, kind, name)
	if !off {
		return "", false
	}
	for deadline, poll := d.now().Add(within), viewPoll; d.now().Before(deadline); poll = min(2*poll, viewPollMax) {
		d.sleep(min(poll, deadline.Sub(d.now())))
		if why, off = offMain(d.screen, kind, name); !off {
			return "", true
		}
	}
	return why, true
}

// offMainRefusal is the message for a pane that never showed its input.
func offMainRefusal(name, why string) string {
	return fmt.Sprintf("%s's pane shows %s; nothing was typed", name, why)
}

// refuseOffMain reports a pane that never showed its input, as code<TAB>message.
func refuseOffMain(stderr io.Writer, name, why string) int {
	fmt.Fprintf(stderr, "agent_input_elsewhere\t%s\n", offMainRefusal(name, why))
	return exitcode.HerdrError
}
