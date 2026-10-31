package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// cmdLaunchSpec resolves configuration and asks the adapter for argv.
//
// Warnings go to stderr as text and stdout stays exactly one JSON object, so
// bash can consume stdout without filtering.
func cmdLaunchSpec(args []string) int {
	fs := flag.NewFlagSet("agent launch-spec", flag.ContinueOnError)
	fs.Bool("dry-run", false, "show the launch without starting the agent")
	l, rc := loadResolved(fs, args)
	if rc != exitcode.OK {
		return rc
	}
	cwd, absRoot, r := l.Cwd, l.Root, l.Resolved
	if l.Reviewer != nil {
		// An unattended reviewer that cannot be resolved is a gate a human has
		// to clear, the same refusal a builder gets for a manual gate.
		if l.Reviewer.Status != config.ReviewerResolved {
			for _, w := range l.Warnings {
				fmt.Fprintf(os.Stderr, "tandem: %s\n", w)
			}
			fmt.Fprintf(os.Stderr, "tandem: reviewer is %s: %s\n", l.Reviewer.Status, l.Reviewer.Reason)
			return exitcode.ManualGate
		}
		r = *l.Reviewer.Resolved
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(os.Stderr, "tandem: %s\n", w)
	}

	// A kind the repository asked for is confirmed once per repository. A kind the
	// user supplied is their own decision and is never challenged.
	if user, err := config.LoadUser(config.UserConfigPath()); err == nil && user.NeedsApproval(absRoot, r) {
		fmt.Fprintf(os.Stderr,
			"tandem: %s asks for %s kind %q, which you have not approved for this repository.\n"+
				"        Approve once with:  tandem-pair --approve-kind %s\n",
			filepath.Base(paths.Manifest(absRoot)), r.Role, r.Kind.Value, r.Kind.Value)
		return exitcode.KindUnapproved
	}

	a, err := agent.Get(r.Kind.Value)
	if err != nil {
		return refuse(err)
	}
	spec := r.Spec(cwd)
	// Only unattended roles get a session id. If entropy cannot be read, the
	// adapter emits no --session-id.
	if r.Role.Unattended() {
		if id, err := newSessionID(); err == nil {
			spec.SessionID = id
		} else {
			fmt.Fprintf(os.Stderr, "tandem: could not generate a session id (%v); "+
				"a copilot builder may stall on the session-restore picker\n", err)
		}
	}
	launch, err := a.Resolve(spec)
	if err != nil {
		return refuse(err)
	}
	for _, w := range launch.Warnings {
		fmt.Fprintf(os.Stderr, "tandem: %s %s=%s -> %s (%s)\n",
			r.Kind.Value, w.Axis, w.Requested, w.Substituted, w.DirectionS)
		if w.Detail != "" {
			fmt.Fprintf(os.Stderr, "        %s\n", w.Detail)
		}
	}
	return emitJSON(launch)
}

// refuse prints the structured error on stderr and returns its exit code, so
// the caller prints the remedy verbatim rather than re-authoring it.
func refuse(err error) int {
	e, ok := err.(*agent.Error)
	if !ok {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	b, _ := json.Marshal(map[string]any{"error": e})
	fmt.Fprintln(os.Stderr, string(b))
	if e.Exit != 0 {
		return e.Exit
	}
	return exitcode.NarrowingRefused
}
