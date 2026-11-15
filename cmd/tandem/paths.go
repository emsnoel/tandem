package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/pairname"
	"github.com/emsnoel/tandem/internal/paths"
)

// cmdPaths prints where tandem keeps one of its files, so the bash entry points
// share internal/paths' legacy-name rule instead of restating it. It prints a
// bare path, since every caller assigns it to a variable.
func cmdPaths(args []string) int {
	const usage = "usage: tandem paths <manifest|relay|names> [--root DIR]\n" +
		"       tandem paths names [--root DIR] [--state-home DIR]   (planner, builder and reviewer names, tab separated)\n" +
		"       tandem paths manifest --from DIR   (nearest manifest at or above DIR, bounded by DIR's git toplevel)"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitcode.Usage
	}
	which := args[0]

	fs := flag.NewFlagSet("paths "+which, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "repository root")
	stateHome := fs.String("state-home", os.Getenv("TANDEM_STATE_HOME"), "names only: state home, replacing the default base directory")
	from := fs.String("from", "", "manifest only: search upward from DIR for the nearest manifest, bounded by DIR's git toplevel; mutually exclusive with --root")
	if err := fs.Parse(args[1:]); err != nil {
		return exitcode.Usage
	}

	var rootGiven, fromGiven bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "root":
			rootGiven = true
		case "from":
			fromGiven = true
		}
	})
	if rootGiven && fromGiven {
		fmt.Fprintln(os.Stderr, "tandem paths: --root and --from are mutually exclusive")
		return exitcode.Usage
	}
	if fromGiven && which != "manifest" {
		fmt.Fprintf(os.Stderr, "tandem paths: --from is only valid for manifest, not %q\n", which)
		return exitcode.Usage
	}

	// Always absolute: a caller may cd before using the answer.
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}

	switch which {
	case "manifest":
		if !fromGiven {
			fmt.Println(paths.Manifest(absRoot))
			break
		}
		m, err := nearestManifest(*from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
			return exitcode.Internal
		}
		fmt.Println(m)
	case "relay":
		fmt.Println(paths.Relay(absRoot))
	case "names":
		n, err := pairname.Resolve(absRoot, *stateHome, os.Getenv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
			return exitcode.Internal
		}
		fmt.Printf("%s\t%s\t%s\n", n.Planner, n.Builder, n.Reviewer)
	default:
		fmt.Fprintf(os.Stderr, "tandem paths: unknown file %q\n%s\n", which, usage)
		return exitcode.Usage
	}
	return exitcode.OK
}
