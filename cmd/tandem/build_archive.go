package main

// tandem build archive: copy the relay's build and its evidence into
// <root>/.tandem-history before anything deletes it. Replacing a build and
// tandem-unpair both use this, so the archive location has one definition.

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

func cmdBuildArchive(args []string) int {
	fs := flag.NewFlagSet("build archive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem build archive --root DIR [--json]")
		return exitcode.Usage
	}
	res, err := build.Archive(*root, build.Dir(paths.Relay(*root)))
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return printJSON(res)
	}
	if res.Dir != "" {
		fmt.Printf("archived %d files to %s\n", res.Files, res.Dir)
	}
	return exitcode.OK
}
