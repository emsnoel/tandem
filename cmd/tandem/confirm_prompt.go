package main

import (
	"fmt"
	"io"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// promptAs is `tandem agent prompt NAME TEXT --as KIND`: it delivers the
// prompt by KIND's policy (senderPolicies, deliver.go). stdout is
// outcome<TAB>status. A failure is code<TAB>message on stderr's first line, as
// without --as; a turn that ended at once adds its pane. A prompt that never
// started also prints never_started<TAB>SEQ on stdout: the pane's lifecycle
// counter when it was typed, for `tandem agent status --since`.
func promptAs(d herdrDeps, name, text string, p deliverPolicy, stderr io.Writer) int {
	res := deliverPrompt(d, deliverReq{name: name, text: text}, p)
	switch res.outcome {
	case sendRefused, sendUncertain:
		if res.code != "" {
			fmt.Fprintf(stderr, "%s\t%v\n", res.code, res.err)
			return exitcode.HerdrError
		}
		fmt.Fprintln(stderr, res.err.Error())
		return herdr.ExitCodeFor(res.err)
	case sendEndedAtOnce:
		fmt.Fprintf(stderr, "agent_turn_failed\t%s's turn ended as soon as it started, so nothing is working on the prompt\n%s\n", name, res.paneText)
		return exitcode.TurnEndedAtOnce
	case sendNeverStarted:
		fmt.Printf("never_started\t%d\n", res.agent.StateChangeSeq)
		return neverStartedExit(stderr, name)
	case sendQueued:
		fmt.Printf("queued\t%s\n", res.agent.StatusString())
	case sendTyped:
		fmt.Printf("typed\t%s\n", res.agent.StatusString())
	default:
		fmt.Printf("started\t%s\n", res.agent.StatusString())
	}
	return exitcode.OK
}

// neverStartedExit reports a prompt that was accepted but showed no start.
func neverStartedExit(stderr io.Writer, name string) int {
	fmt.Fprintf(stderr, "agent_never_started\tthe prompt was accepted but %s never started working; it may be at a native dialog Herdr cannot see\n", name)
	return exitcode.NeverStarted
}

// startedSince reports whether a pane has begun a turn since a prompt was typed
// into it, when its lifecycle counter read seq. Any change of the counter
// counts, even one back to a lower number after Herdr restarted, and so does a
// pane that is not settled now. A prompt typed into a planner that is busy in a
// way Herdr does not show starts once that work ends, after the confirm window.
func startedSince(a *herdr.Agent, seq int) bool {
	return a.StateChangeSeq != seq || !agent.Settled(a.StatusString())
}
