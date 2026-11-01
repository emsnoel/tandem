package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/agent/trust"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func cmdTrust(args []string) int {
	if len(args) == 0 || args[0] != "clean" {
		fmt.Fprintln(os.Stderr, "usage: tandem trust clean --kind K --cwd DIR")
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("trust clean", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("kind", "", "agent kind")
	cwd := fs.String("cwd", "", "directory whose trust entry to remove")
	if err := fs.Parse(args[1:]); err != nil {
		return exitcode.Usage
	}
	if *kind == "" || *cwd == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem trust clean --kind K --cwd DIR")
		return exitcode.Usage
	}
	removed, err := trust.Clean(*kind, *cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	// A token, since bin/tandem-unpair is the only caller.
	if removed {
		fmt.Println("removed")
	} else {
		fmt.Println("none")
	}
	return exitcode.OK
}
