package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func cmdApprove(args []string) int {
	fs := flag.NewFlagSet("config approve-kind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "repository root")
	kind := fs.String("kind", "", "agent kind to approve for this repository")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if *kind == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem config approve-kind --root DIR --kind K")
		return exitcode.Usage
	}
	abs := config.CanonicalKey(*root)
	if err := config.ApproveKind(config.UserConfigPath(), abs, *kind); err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	return emitJSON(map[string]any{"approved": *kind, "repo": abs})
}
