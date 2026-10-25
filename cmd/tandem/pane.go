package main

import (
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// paneArgs prepends "pane" to the subcommand args.
func paneArgs(args []string) []string {
	return append([]string{"pane"}, args...)
}

// cmdPane handles `tandem pane <subcommand> ...`.
func cmdPane(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem pane <close|layout|process-info|rename|split|report-metadata|wait-shell> ...")
		return exitcode.Usage
	}
	switch args[0] {
	case "close", "layout", "process-info", "rename", "split", "report-metadata":
		return passthrough(paneArgs(args)...)
	case "wait-shell":
		return cmdPaneWaitShell(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tandem pane: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
}
