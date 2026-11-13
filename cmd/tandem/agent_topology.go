package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
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
// It prints pane, cwd, tab, name and kind as NUL fields; the kind is empty for
// a named pane Herdr does not list. It exits NoPlanner with nothing printed,
// ManyPlanners with pane, name and cwd for each candidate, or NotAPlanner
// with the reason on stderr.
func cmdAgentPlanner(args []string) int {
	fs := flag.NewFlagSet("agent planner", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pane := fs.String("pane", "", "use this pane as the planner")
	kind := fs.String("kind", "", "the kind to pick when several agents could plan (default: defaults.planner_kind, or HERDR_PLANNER_KIND)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem agent planner [--pane ID] [--kind KIND]")
		return exitcode.Usage
	}
	prefer, rc := preferredPlannerKind(*kind)
	if rc != exitcode.OK {
		return rc
	}
	agents, rc := listAgents()
	if rc != exitcode.OK {
		return rc
	}
	a, cands, err := herdr.PickPlanner(agents, *pane, os.Getenv("HERDR_PANE_ID"), agent.PlannerKinds, prefer)
	switch {
	case errors.Is(err, herdr.ErrNoPlanner):
		return exitcode.NoPlanner
	case errors.Is(err, herdr.ErrManyPlanners):
		for _, c := range cands {
			writeFields(os.Stdout, c.PaneID, nameOf(c), c.CWD)
		}
		return exitcode.ManyPlanners
	}
	if a.Kind != "" && !agent.CanPlan(a.Kind) {
		return refuseNonPlanner(fmt.Sprintf("pane %s runs %s", a.PaneID, a.Kind), a.Kind)
	}
	writeFields(os.Stdout, a.PaneID, a.CWD, a.TabID, nameOf(a), a.Kind)
	return exitcode.OK
}

// preferredPlannerKind is the kind that breaks a tie between agents that could
// plan: the flag, else HERDR_PLANNER_KIND, else defaults.planner_kind. Empty
// means any kind that can plan. A kind that cannot plan is refused.
func preferredPlannerKind(flagKind string) (string, int) {
	path := config.UserConfigPath()
	user, err := config.LoadUser(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return "", exitcode.Usage
	}
	r := config.ResolveRole(agent.RolePlanner, &config.Repo{}, user, os.Getenv, config.Overrides{Kind: flagKind})
	if r.Kind.From == config.FromDefault {
		return "", exitcode.OK
	}
	if !agent.CanPlan(r.Kind.Value) {
		return "", refuseNonPlanner(fmt.Sprintf("the planner kind is %s (from %s)", r.Kind.Value, r.Kind.From), r.Kind.Value)
	}
	return r.Kind.Value, exitcode.OK
}

func refuseNonPlanner(what, kind string) int {
	fmt.Fprintf(os.Stderr, "%s, and %s can't plan. The planner must be one of %s. %s can still build and review.\n",
		what, kind, strings.Join(agent.PlannerKinds, ", "), kind)
	return exitcode.NotAPlanner
}

// cmdAgentAdoptable prints the pane and kind of the one unnamed agent
// bin/tandem-pair may adopt as the builder, or nothing. An unnamed agent beside
// the planner with a kind tandem cannot run exits NarrowingRefused and prints
// that agent's pane and kind instead, so the person learns why it was not used.
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
	for _, a := range herdr.Unnamed(agents, *exclude, *tab) {
		if agent.FirstUnsupported(a.Kind) != "" {
			writeFields(os.Stdout, a.PaneID, a.Kind)
			return exitcode.NarrowingRefused
		}
	}
	if a := herdr.Adoptable(agents, *exclude, *tab, *kind); a != nil {
		writeFields(os.Stdout, a.PaneID, a.Kind)
	}
	return exitcode.OK
}

// cmdAgentKind prints the kind Herdr detected for the agent named NAME, or in
// pane P with --pane.
func cmdAgentKind(args []string) int {
	var pane string
	switch {
	case len(args) == 2 && args[0] == "--pane" && args[1] != "":
		pane = args[1]
	case len(args) != 1 || strings.HasPrefix(args[0], "-"):
		fmt.Fprintln(os.Stderr, "usage: tandem agent kind NAME | --pane PANE")
		return exitcode.Usage
	}
	agents, rc := listAgents()
	if rc != exitcode.OK {
		return rc
	}
	var a *herdr.Agent
	if pane != "" {
		if a = herdr.FindPane(agents, pane); a == nil {
			fmt.Fprintf(os.Stderr, "not_found\tno agent in pane %q\n", pane)
			return exitcode.HerdrError
		}
	} else if a = herdr.Find(agents, args[0]); a == nil {
		fmt.Fprintf(os.Stderr, "not_found\tno agent named %q\n", args[0])
		return exitcode.HerdrError
	}
	fmt.Println(a.Kind)
	return exitcode.OK
}

// cmdAgentCount answers bin/tandem-unpair's --clean-trust question: how many
// other agents of a kind still work in the repo. It prints the count, or
// nothing when Herdr fails.
func cmdAgentCount(args []string) int {
	fs := flag.NewFlagSet("agent count", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("kind", "", "count agents of this kind")
	under := fs.String("under", "", "count agents working in this directory or below it")
	var exclude listFlag
	fs.Var(&exclude, "exclude-pane", "leave out this pane (repeatable)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *kind == "" || *under == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem agent count --kind KIND --under DIR [--exclude-pane PANE]...")
		return exitcode.Usage
	}
	agents, rc := listAgents()
	if rc != exitcode.OK {
		return rc
	}
	fmt.Println(herdr.CountUnder(agents, *kind, *under, exclude))
	return exitcode.OK
}
