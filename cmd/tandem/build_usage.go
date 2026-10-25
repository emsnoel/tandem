package main

// tandem build usage: what a durable run's model jobs consumed.
//
//	tandem build usage [--root DIR] [--state-home DIR] [--sessions-dir DIR] [--json] [--records]
//
// It reads the agents' logs (Codex rollout logs under --sessions-dir, default
// $CODEX_HOME/sessions) without changing them, stores each event once under its
// source id, and reports the total and the figure by role. A total that is
// partial or unavailable says so, shows no derived uncached figure, and never
// shows a missing count as zero.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/vcs"
	"github.com/emsnoel/tandem/internal/workflow"
)

// usageSourceFlags registers where each builder kind's CLI writes its logs, with
// each CLI's default, and returns the usage source once flags are parsed. One
// place for every kind, so no command leaves one out.
func usageSourceFlags(fs *flag.FlagSet) func() controller.AgentUsageSource {
	src := controller.DefaultAgentUsageSource()
	fs.StringVar(&src.CodexSessions, "sessions-dir", src.CodexSessions, "where Codex files its session logs")
	fs.StringVar(&src.ClaudeProjects, "claude-projects", src.ClaudeProjects, "where Claude Code files its conversation logs")
	fs.StringVar(&src.CopilotHome, "copilot-home", src.CopilotHome, "where Copilot CLI keeps its logs and conversations")
	return func() controller.AgentUsageSource { return src }
}

func cmdBuildUsage(args []string) int {
	fs := flag.NewFlagSet("build usage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	logs := usageSourceFlags(fs)
	asJSON := fs.Bool("json", false, "print one JSON object")
	records := fs.Bool("records", false, "include every usage record, with its source, provider, model and configuration")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem build usage [--root DIR] [--state-home DIR] [--sessions-dir DIR] [--claude-projects DIR] [--copilot-home DIR] [--json] [--records]")
		return exitcode.Usage
	}
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tandem build usage: not inside a git checkout; pass --root")
			return exitcode.Usage
		}
		f.root = top
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	c, err := controller.New(controller.Dependencies{State: d.st, Artifacts: d.art, Transport: noSend{}, CheckExecutor: controller.NewCheckExecutor(d.art),
		Owner: cliOwner, UsageSource: logs()})
	if err != nil {
		return fail(err)
	}
	rep, err := c.UsageReport(context.Background(), d.run.RunID, *records)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return printJSON(rep)
	}
	fmt.Printf("run %s\n", rep.RunID)
	fmt.Printf("total      %s\n", usageLine(rep.Total))
	roles := make([]string, 0, len(rep.ByRole))
	for r := range rep.ByRole {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		fmt.Printf("%-10s %s\n", r, usageLine(rep.ByRole[r]))
	}
	fmt.Printf("sources    %d\n", rep.Sources)
	for _, n := range rep.Notes {
		fmt.Printf("note: %s\n", n)
	}
	return exitcode.OK
}

// usageLine prints a figure with unknowns as "unknown", never as zero, and the
// uncached part only when it can be backed.
func usageLine(u workflow.Usage) string {
	num := func(p *int64) string {
		if p == nil {
			return "unknown"
		}
		return fmt.Sprint(*p)
	}
	parts := []string{"input " + num(u.Input), "cached " + num(u.CachedInput), "output " + num(u.Output)}
	if v, ok := u.Uncached(); ok {
		parts = append(parts, fmt.Sprintf("uncached %d", v))
	}
	parts = append(parts, u.Completeness)
	return strings.Join(parts, "  ")
}
