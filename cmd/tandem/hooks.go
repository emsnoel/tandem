package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/hooks"
	"github.com/emsnoel/tandem/internal/paths"
)

func cmdHooks(args []string) int {
	if len(args) == 0 || args[0] != "classify" {
		fmt.Fprintln(os.Stderr, "usage: tandem hooks classify [--codex-home DIR]")
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	home := fs.String("codex-home", "", "Codex home directory")
	if err := fs.Parse(args[1:]); err != nil {
		return exitcode.Usage
	}
	h := *home
	if h == "" {
		// $CODEX_HOME, else ~/.codex.
		h = paths.CodexHome()
	}
	if h == "" {
		fmt.Fprintln(os.Stderr, "tandem: cannot locate a Codex home; pass --codex-home")
		return exitcode.Internal
	}
	for _, finding := range hooks.Classify(h) {
		fmt.Printf("%s\t%s\n", finding.Kind, finding.Path)
	}
	return exitcode.OK
}
