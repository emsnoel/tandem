package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// snapshotLine renders one agent as pane_id<TAB>cwd<TAB>agent_status. A tab
// inside a field is an error, since bash's `read` would shift the fields.
func snapshotLine(a *herdr.Agent) (string, error) {
	status := "unknown"
	if a.Status != nil {
		status = *a.Status
	}
	for _, f := range []string{a.PaneID, a.CWD, status} {
		if strings.ContainsAny(f, "\t\n") {
			return "", fmt.Errorf("agent field contains a tab or newline: %q", f)
		}
	}
	return a.PaneID + "\t" + a.CWD + "\t" + status, nil
}

// stringList collects a flag given more than once, e.g. --until idle --until done.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// promptArgs, readArgs, waitArgs, renameArgs and agentArgs build herdr's
// command-line arguments.
func promptArgs(name, text string, wait bool, until []string, timeoutMS int) []string {
	a := []string{"agent", "prompt", name, text}
	if wait {
		a = append(a, "--wait")
	}
	for _, u := range until {
		a = append(a, "--until", u)
	}
	if timeoutMS > 0 {
		a = append(a, "--timeout", strconv.Itoa(timeoutMS))
	}
	return a
}

func readArgs(name, source string, lines int) []string {
	a := []string{"agent", "read", name}
	if source != "" {
		a = append(a, "--source", source)
	}
	if lines > 0 {
		a = append(a, "--lines", strconv.Itoa(lines))
	}
	return a
}

func waitArgs(name string, until []string, timeoutMS int) []string {
	a := []string{"agent", "wait", name}
	for _, u := range until {
		a = append(a, "--until", u)
	}
	if timeoutMS > 0 {
		a = append(a, "--timeout", strconv.Itoa(timeoutMS))
	}
	return a
}

func renameArgs(target string, tail []string) []string {
	return append([]string{"agent", "rename", target}, tail...)
}

func agentArgs(sub string, tail []string) []string {
	return append([]string{"agent", sub}, tail...)
}

// passthrough runs herdr and copies stdout through unchanged.
func passthrough(args ...string) int {
	out, err := herdr.Raw(args...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	_, _ = os.Stdout.Write(out)
	return exitcode.OK
}

func cmdAgent(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem agent <snapshot|status|session|reset|explain-reset|prompt|read|wait|alive|watch|self|start|launch-spec|prelaunch|kinds|instruction-files|planner|adoptable|kind> ...")
		return exitcode.Usage
	}
	sub, rest := args[0], args[1:]
	if sub == "self" {
		return cmdAgentSelf(rest)
	}
	if sub == "reset" {
		return cmdAgentReset(rest)
	}
	if sub == "explain-reset" {
		return cmdAgentExplainReset(rest)
	}
	if sub == "session" {
		return cmdAgentSession(rest)
	}
	switch sub {
	case "planner":
		return cmdAgentPlanner(rest)
	case "adoptable":
		return cmdAgentAdoptable(rest)
	case "kind":
		return cmdAgentKind(rest)
	}
	if sub == "launch-spec" {
		return cmdLaunchSpec(rest)
	}
	if sub == "prelaunch" {
		return cmdPrelaunch(rest)
	}
	if sub == "instruction-files" {
		// The repo-root files this kind's CLI loads into every conversation, one per
		// line. bin/tandem-handoff skips asking the builder to read these.
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: tandem agent instruction-files KIND")
			return exitcode.Usage
		}
		a, err := agent.Get(rest[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return exitcode.Usage
		}
		for _, f := range a.InstructionFiles() {
			fmt.Println(f)
		}
		return exitcode.OK
	}
	if sub == "watch" {
		return cmdAgentWatch(rest)
	}
	if sub == "alive" {
		// tandem agent alive NAME: watch an agent that was just started.
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: tandem agent alive NAME")
			return exitcode.Usage
		}
		return agentAliveCmd(liveHerdrDeps(transportSpec{}), rest[0], failWithinFromEnv(), os.Stderr)
	}
	if sub == "kinds" {
		// One registered kind per line, sorted. tests/test-kind-drift.sh checks
		// hand-written kind lists against it.
		for _, k := range agent.Kinds() {
			fmt.Println(k)
		}
		return exitcode.OK
	}

	// start and list forward verbatim and take no name.
	if sub == "start" || sub == "list" {
		return passthrough(agentArgs(sub, rest)...)
	}
	if sub == "rename" {
		if len(rest) < 2 {
			fmt.Fprintln(os.Stderr, "tandem agent rename: need a target and a name or --clear")
			return exitcode.Usage
		}
		return passthrough(renameArgs(rest[0], rest[1:])...)
	}

	// Herdr puts flags after positionals, while Go's flag package stops at the
	// first positional, so slice off the fixed positionals first.
	arity := 1
	if sub == "prompt" {
		arity = 2
	}
	if len(rest) < arity {
		fmt.Fprintf(os.Stderr, "tandem agent %s: need %d positional argument(s)\n", sub, arity)
		return exitcode.Usage
	}
	pos, flagArgs := rest[:arity], rest[arity:]
	name := pos[0]

	fs := flag.NewFlagSet("agent "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	wait := fs.Bool("wait", false, "wait for the first settled state after submission")
	as := fs.String("as", "", "prompt: deliver as this kind of message, with its checks (deliver.go): "+strings.Join(senderKinds(), "|"))
	waitUnblocked := fs.Int("wait-unblocked", 0, "with --as: wait this many ms for a dialog or a subagent's screen to clear, in place of the kind's own wait")
	timeoutMS := fs.Int("timeout", 0, "fail after this many milliseconds")
	lines := fs.Int("lines", 0, "how many lines of pane output to read")
	var until stringList
	fs.Var(&until, "until", "state to match after --wait; repeat for more than one")
	source := fs.String("source", "", "visible|recent|recent-unwrapped|detection")
	if err := fs.Parse(flagArgs); err != nil {
		return exitcode.Usage
	}

	switch sub {
	case "snapshot":
		agents, err := herdr.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return herdr.ExitCodeFor(err)
		}
		a := herdr.Find(agents, name)
		if a == nil {
			fmt.Fprintf(os.Stderr, "not_found\tno agent named %q\n", name)
			return exitcode.HerdrError
		}
		line, err := snapshotLine(a)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return exitcode.Internal
		}
		fmt.Println(line)
		return exitcode.OK

	case "status":
		a, err := herdr.Get(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return herdr.ExitCodeFor(err)
		}
		if a.Status == nil {
			fmt.Println("unknown")
			return exitcode.OK
		}
		fmt.Println(*a.Status)
		return exitcode.OK

	case "prompt":
		waitSet := false
		fs.Visit(func(f *flag.Flag) { waitSet = waitSet || f.Name == "wait-unblocked" })
		if *as != "" {
			p, ok := senderPolicies[*as]
			if !ok {
				fmt.Fprintf(os.Stderr, "tandem agent prompt: --as must be one of %s, not %q\n", strings.Join(senderKinds(), ", "), *as)
				return exitcode.Usage
			}
			if *wait || len(until) > 0 || *timeoutMS > 0 {
				fmt.Fprintln(os.Stderr, "tandem agent prompt: --as does its own waiting; it takes no --wait, --until or --timeout")
				return exitcode.Usage
			}
			if waitSet {
				if *waitUnblocked < 0 {
					fmt.Fprintln(os.Stderr, "tandem agent prompt: --wait-unblocked takes a number of milliseconds")
					return exitcode.Usage
				}
				p.wait = time.Duration(*waitUnblocked) * time.Millisecond
			}
			return promptAs(liveHerdrDeps(transportSpec{}), name, pos[1], p, os.Stderr)
		}
		if waitSet {
			fmt.Fprintln(os.Stderr, "tandem agent prompt: --wait-unblocked is valid only with --as")
			return exitcode.Usage
		}
		// Herdr types into whatever screen the pane shows (confirm_view.go).
		if a, err := herdr.Get(name); err == nil {
			if why, off := offMain(liveScreen, a.Kind, name); off {
				return refuseOffMain(os.Stderr, name, why)
			}
		}
		return passthrough(promptArgs(name, pos[1], *wait, until, *timeoutMS)...)

	case "read":
		return passthrough(readArgs(name, *source, *lines)...)

	case "wait":
		return passthrough(waitArgs(name, until, *timeoutMS)...)

	default:
		fmt.Fprintf(os.Stderr, "tandem agent: unknown subcommand %q\n", sub)
		return exitcode.Usage
	}
}
