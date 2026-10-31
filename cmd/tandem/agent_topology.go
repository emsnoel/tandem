package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

func nameOf(a herdr.Agent) string {
	if a.Name == nil {
		return ""
	}
	return *a.Name
}

// listAgents lists Herdr's agents once, printing the error and returning its
// exit code on failure.
func listAgents() ([]herdr.Agent, int) {
	agents, err := herdr.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return nil, herdr.ExitCodeFor(err)
	}
	return agents, exitcode.OK
}

// cmdAgentPlanner answers bin/tandem-pair's "which pane is the planner".
// It prints pane, cwd, tab and name as NUL fields. It exits NoPlanner with
// nothing printed, or ManyPlanners with pane, name and cwd for each candidate.
func cmdAgentPlanner(args []string) int {
	fs := flag.NewFlagSet("agent planner", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pane := fs.String("pane", "", "use this pane as the planner")
	kind := fs.String("kind", "claude", "the planner's agent kind")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem agent planner [--pane ID] [--kind KIND]")
		return exitcode.Usage
	}
	agents, rc := listAgents()
	if rc != exitcode.OK {
		return rc
	}
	a, cands, err := herdr.PickPlanner(agents, *pane, os.Getenv("HERDR_PANE_ID"), *kind)
	switch {
	case errors.Is(err, herdr.ErrNoPlanner):
		return exitcode.NoPlanner
	case errors.Is(err, herdr.ErrManyPlanners):
		for _, c := range cands {
			writeFields(os.Stdout, c.PaneID, nameOf(c), c.CWD)
		}
		return exitcode.ManyPlanners
	}
	writeFields(os.Stdout, a.PaneID, a.CWD, a.TabID, nameOf(a))
	return exitcode.OK
}

// cmdAgentAdoptable prints the pane and kind of the one unnamed agent
// bin/tandem-pair may adopt as the builder, or nothing.
func cmdAgentAdoptable(args []string) int {
	fs := flag.NewFlagSet("agent adoptable", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	exclude := fs.String("exclude-pane", "", "the planner's pane, never adopted")
	tab := fs.String("tab", "", "adopt only from this tab")
	kind := fs.String("kind", "", "adopt only an agent of this kind")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *exclude == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem agent adoptable --exclude-pane PANE [--tab TAB] [--kind KIND]")
		return exitcode.Usage
	}
	agents, rc := listAgents()
	if rc != exitcode.OK {
		return rc
	}
	if a := herdr.Adoptable(agents, *exclude, *tab, *kind); a != nil {
		writeFields(os.Stdout, a.PaneID, a.Kind)
	}
	return exitcode.OK
}

// cmdAgentKind prints the kind Herdr detected for the agent called name.
func cmdAgentKind(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: tandem agent kind NAME")
		return exitcode.Usage
	}
	agents, rc := listAgents()
	if rc != exitcode.OK {
		return rc
	}
	a := herdr.Find(agents, args[0])
	if a == nil {
		fmt.Fprintf(os.Stderr, "not_found\tno agent named %q\n", args[0])
		return exitcode.HerdrError
	}
	fmt.Println(a.Kind)
	return exitcode.OK
}
