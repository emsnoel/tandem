package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// selfName is the agent in pane, or "" when none. With no pane there is no
// agent.
func selfName(agents []herdr.Agent, pane string) string {
	if pane == "" {
		return ""
	}
	for _, a := range agents {
		if a.PaneID == pane && a.Name != nil {
			return *a.Name
		}
	}
	return ""
}

// reviewerVerdict says whether a shell may record a review as the reviewer
// pane. Reason is one of:
//
//	pane        Herdr names this pane the reviewer (Detail: the name)
//	role        no HERDR_PANE_ID, so the claimed role decided (Detail: the role)
//	unreachable Herdr's socket cannot be reached from here, so the claimed
//	            role decided and the caller warns (Detail: the role)
//	other       Herdr names this pane another agent (Detail: that name)
//	unnamed     Herdr lists no named agent in this pane
//	timeout     Herdr did not answer in time
//	error       Herdr failed some other way (Detail: the error)
type reviewerVerdict struct {
	Accept bool
	Reason string
	Detail string
}

// decideReviewer is the check bin/tandem-verdict and bin/tandem-handback
// --review share. Where the pane is known, Herdr's name for it decides, and a
// failed lookup refuses. The claimed role decides only with no pane id or an
// unreachable socket. A sandboxed Codex reviewer can carry another pane's
// HERDR_PANE_ID from its shared daemon while its sandbox denies the socket.
func decideReviewer(pane, claimed, expect string, lookup func() ([]herdr.Agent, error)) reviewerVerdict {
	byRole := func(reason string) reviewerVerdict {
		if claimed != "" && claimed == expect {
			return reviewerVerdict{true, reason, claimed}
		}
		return reviewerVerdict{false, "role", claimed}
	}
	if pane == "" {
		return byRole("role")
	}
	agents, err := lookup()
	switch {
	case err == nil:
	case herdr.IsTimeout(err):
		return reviewerVerdict{false, "timeout", ""}
	case herdr.SocketUnreachable(err):
		return byRole("unreachable")
	default:
		return reviewerVerdict{false, "error", err.Error()}
	}
	switch name := selfName(agents, pane); name {
	case "":
		return reviewerVerdict{false, "unnamed", ""}
	case expect:
		return reviewerVerdict{true, "pane", name}
	default:
		return reviewerVerdict{false, "other", name}
	}
}

// cmdAgentSelf prints the name of the agent in this pane. With --expect it
// prints decideReviewer's verdict instead, as accept|refuse<TAB>REASON<TAB>
// DETAIL, and exits 0 either way: the calling script explains it.
func cmdAgentSelf(args []string) int {
	fs := flag.NewFlagSet("agent self", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	expect := fs.String("expect", "", "the reviewer pane's recorded name")
	claimed := fs.String("claimed", "", "the role the caller claims (--role or HERDR_ROLE)")
	timeout := fs.Int("timeout", 5000, "with --expect: milliseconds to wait for Herdr")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *timeout <= 0 || (*expect == "" && *claimed != "") {
		fmt.Fprintln(os.Stderr, "usage: tandem agent self [--expect NAME [--claimed ROLE] [--timeout MS]]")
		return exitcode.Usage
	}
	if *expect != "" {
		within := time.Duration(*timeout) * time.Millisecond
		v := decideReviewer(os.Getenv("HERDR_PANE_ID"), *claimed, *expect, func() ([]herdr.Agent, error) {
			return herdr.ListWithin(within)
		})
		word := "refuse"
		if v.Accept {
			word = "accept"
		}
		fmt.Printf("%s\t%s\t%s\n", word, v.Reason, v.Detail)
		return exitcode.OK
	}
	agents, err := herdr.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	if n := selfName(agents, os.Getenv("HERDR_PANE_ID")); n != "" {
		fmt.Println(n)
	}
	return exitcode.OK
}
