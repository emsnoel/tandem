package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// paneArgs prepends "pane" to the subcommand args.
func paneArgs(args []string) []string {
	return append([]string{"pane"}, args...)
}

// cmdPane handles `tandem pane <subcommand> ...`.
func cmdPane(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem pane <close|layout|process-info|rename|split|split-direction|report-metadata|wait-shell> ...")
		return exitcode.Usage
	}
	switch args[0] {
	case "close", "layout", "process-info", "rename", "report-metadata":
		return passthrough(paneArgs(args)...)
	case "split":
		return cmdPaneSplit(args)
	case "split-direction":
		return cmdPaneSplitDirection(args[1:])
	case "wait-shell":
		return cmdPaneWaitShell(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tandem pane: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
}

// cmdPaneSplit forwards to `herdr pane split` and prints only the new pane's
// id, which is all bin/tandem-pair needs from the answer.
func cmdPaneSplit(args []string) int {
	out, err := herdr.Raw(paneArgs(args)...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	id, err := herdr.SplitPaneID(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	fmt.Println(id)
	return exitcode.OK
}

// cmdPaneSplitDirection prints the direction to split PANE in, then its width
// and height in cells, as NUL fields. It exits HerdrError when the size is
// unknown; bin/tandem-pair then splits right.
func cmdPaneSplitDirection(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: tandem pane split-direction PANE")
		return exitcode.Usage
	}
	out, err := herdr.Raw("pane", "layout", "--pane", args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	w, h, ok := herdr.PaneSize(out, args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "tandem: herdr gave no size for pane %s\n", args[0])
		return exitcode.HerdrError
	}
	writeFields(os.Stdout, herdr.SplitDirection(w, h), strconv.Itoa(w), strconv.Itoa(h))
	return exitcode.OK
}
