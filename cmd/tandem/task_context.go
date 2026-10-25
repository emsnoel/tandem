package main

// tandem task context --attempt ID --explain
//
// Shows what went into an attempt's prompt, from the manifest persisted when the
// prompt was built, never from the memory as it is now. It reads no model and
// calls none.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/vcs"
)

const taskUse = "usage: tandem task context --attempt ID --explain [--json] [--root DIR] [--state-home DIR]"

func cmdTask(args []string) int {
	if len(args) == 0 || args[0] != "context" {
		fmt.Fprintln(os.Stderr, taskUse)
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("task context", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	attempt := fs.String("attempt", "", "the attempt whose packet to explain")
	explain := fs.Bool("explain", false, "show how the packet was selected")
	asJSON := fs.Bool("json", false, "print one JSON object")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || !*explain || !idRE.MatchString(*attempt) {
		fmt.Fprintln(os.Stderr, taskUse)
		return exitcode.Usage
	}
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tandem task context: not inside a git checkout; pass --root")
			return exitcode.Usage
		}
		f.root = top
	}
	run, err := controller.ReadActiveRun(paths.Relay(f.root))
	if err != nil || run == nil {
		return fail(fmt.Errorf("no durable run in %s", paths.Relay(f.root)))
	}
	id, bound, err := state.LookupIdentity(f.root, f.home)
	if err != nil {
		return fail(err)
	}
	if !bound {
		return fail(fmt.Errorf("no durable state for this checkout reachable from here; `tandem artifact read` works through the relay where this does not"))
	}
	ctx := context.Background()
	st, err := state.OpenReadOnly(ctx, id.StateRoot)
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		return fail(err)
	}
	x, err := controller.ExplainContext(ctx, st, art, run.RunID, *attempt)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return printJSON(x)
	}
	fmt.Printf("attempt %s (task %s)\n", x.AttemptID, x.TaskID)
	fmt.Printf("method %s, limit %d bytes\n", x.Method, x.Limit)
	fmt.Printf("estimated: required %d, optional %d, total %d\n", x.Estimated.Required, x.Estimated.Optional, x.Estimated.Total)
	var sections []string
	for _, s := range x.Sections {
		if s.Bytes > 0 {
			sections = append(sections, fmt.Sprintf("%s %d", s.Name, s.Bytes))
		}
	}
	fmt.Printf("sections: %s\n", strings.Join(sections, ", "))
	list := func(title string, facts []controller.ExplainedFact) {
		fmt.Printf("%s (%d)\n", title, len(facts))
		for _, f := range facts {
			req := ""
			if f.Required {
				req = " required"
			}
			fmt.Printf("  %s %s%s %s %s value %s\n", f.ID[:12], f.Reason, req, f.Kind, f.Trust, f.ValueHash)
		}
	}
	list("selected", x.Selected)
	list("omitted", x.Omitted)
	fmt.Printf("prompt %s\nconfig %s\nmanifest %s\n", x.PromptHash, x.ConfigHash, x.ManifestHash)
	return exitcode.OK
}
