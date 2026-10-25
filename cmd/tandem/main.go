// Command tandem is the compiled core beneath the bash entry points, and the
// one command a human types to reach them.
//
// Go decides and bash explains: compiled subcommands return a verdict or a
// value, never prose. A verb this binary does not implement is handed to a
// tandem-<verb> script on PATH (see dispatch.go).
package main

import (
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitcode.Usage
	}
	switch args[0] {
	case "version":
		return cmdVersion(args[1:])
	case "agent":
		return cmdAgent(args[1:])
	case "pane":
		return cmdPane(args[1:])
	case "tab":
		return cmdTab(args[1:])
	case "hooks":
		return cmdHooks(args[1:])
	case "gate":
		return cmdGate(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "decision":
		return cmdDecision(args[1:])
	case "triage":
		return cmdTriage(args[1:])
	case "paths":
		return cmdPaths(args[1:])
	case "reviewer":
		return cmdReviewer(args[1:])
	case "trust":
		return cmdTrust(args[1:])
	case "state":
		return cmdState(args[1:])
	case "usage":
		return cmdUsage(args[1:])
	case "build":
		return cmdBuild(args[1:])
	case "collaboration":
		return cmdCollaboration(args[1:])
	case "check":
		return cmdCheck(args[1:])
	case "artifact":
		return cmdArtifact(args[1:])
	case "task":
		return cmdTask(args[1:])

	default:
		// Not a compiled subcommand: hand it to a bash entry point.
		return delegate(args[0], args[1:])
	}
}
