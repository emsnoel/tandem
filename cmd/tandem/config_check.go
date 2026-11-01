package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/vcs"
)

// nearestManifest is the manifest `tandem paths manifest --from DIR` names:
// the nearest one at or above from, bounded by from's git toplevel, or the one
// in from itself outside a repository.
func nearestManifest(from string) (string, error) {
	abs, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	boundary, err := vcs.Toplevel(abs)
	if err != nil {
		return paths.Manifest(abs), nil
	}
	return paths.NearestManifest(abs, boundary), nil
}

// cmdConfigCheckCommand answers "what is the check command, and which paths
// does it cover" for bin/tandem-unpair and bin/tandem-project. With --fields
// it prints the command, then each path, every one NUL-terminated.
func cmdConfigCheckCommand(args []string) int {
	const usage = "usage: tandem config check-command --root DIR [--from DIR] [--fields]"
	fs := flag.NewFlagSet("config check-command", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "the directory detection looks in")
	from := fs.String("from", "", "where the manifest search starts (default: --root)")
	asFields := fs.Bool("fields", false, "print NUL-separated fields for bash in place of JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *root == "" {
		fmt.Fprintln(os.Stderr, usage)
		return exitcode.Usage
	}
	if *from == "" {
		*from = *root
	}
	manifest, err := nearestManifest(*from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	c := config.ResolveCheck(*root, manifest)
	if *asFields {
		writeFields(os.Stdout, append([]string{c.Command}, c.Paths...)...)
		return exitcode.OK
	}
	if c.Paths == nil {
		c.Paths = []string{}
	}
	return emitJSON(map[string]any{"check_command": c.Command, "check_paths": c.Paths})
}
