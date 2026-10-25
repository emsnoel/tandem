package main

import (
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// sourceHash is stamped at build time with -ldflags by install.sh --from-source
// or tools/package-release.sh. Empty means built by hand, which tests/run.sh
// reports as stale.
var sourceHash = ""

// version is the release tag, stamped the same way by tools/package-release.sh.
// A build from a checkout keeps "dev".
var version = "dev"

func cmdVersion(args []string) int {
	if len(args) == 1 && args[0] == "--hash" {
		fmt.Println(sourceHash)
		return exitcode.OK
	}
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem version [--hash]")
		return exitcode.Usage
	}
	fmt.Printf("tandem %s (source %s)\n", version, shortOrNone(sourceHash))
	return exitcode.OK
}

// shortOrNone shows a short, human-readable form of a (possibly empty) hash.
func shortOrNone(h string) string {
	if h == "" {
		return "unstamped"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
