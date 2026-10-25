package main

import (
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// cmdTab handles `tandem tab <get|rename> ...`. Both forward to Herdr
// unchanged: bin/tandem-pair reads a tab's label to see whether a person has
// named it, and names it only when they have not.
func cmdTab(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem tab <get|rename> ...")
		return exitcode.Usage
	}
	switch args[0] {
	case "get", "rename":
		return passthrough(append([]string{"tab"}, args...)...)
	default:
		fmt.Fprintf(os.Stderr, "tandem tab: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
}
