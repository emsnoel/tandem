package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// cmdStateActiveRun answers bin/tandem-unpair's "which durable run is
// active": it prints the run id from the relay's marker, or nothing when there
// is no marker. A marker it cannot read exits ManualGate, as `tandem state
// engine` does for the same file.
func cmdStateActiveRun(args []string) int {
	fs := flag.NewFlagSet("state active-run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "", "the relay mailbox holding active-run.json")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem state active-run --relay DIR")
		return exitcode.Usage
	}
	run, err := controller.ReadActiveRun(*relay)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem state active-run: %v\n", err)
		return exitcode.ManualGate
	}
	if run != nil {
		fmt.Println(run.RunID)
	}
	return exitcode.OK
}
