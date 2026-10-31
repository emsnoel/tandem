package main

import (
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// cmdTab handles `tandem tab <get|rename|label> ...`. get and rename forward to
// Herdr unchanged. label prints `default` for a tab still labelled with its
// number and `named` for one a person named, so bin/tandem-pair names only the
// first kind.
func cmdTab(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem tab <get|rename|label> ...")
		return exitcode.Usage
	}
	switch args[0] {
	case "get", "rename":
		return passthrough(append([]string{"tab"}, args...)...)
	case "label":
		return cmdTabLabel(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tandem tab: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
}

func cmdTabLabel(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: tandem tab label TAB")
		return exitcode.Usage
	}
	out, err := herdr.Raw("tab", "get", args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	def, err := herdr.TabIsDefault(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	if def {
		fmt.Println("default")
	} else {
		fmt.Println("named")
	}
	return exitcode.OK
}
