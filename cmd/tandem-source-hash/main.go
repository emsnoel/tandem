// Command tandem-source-hash prints the fingerprint of tandem's build inputs.
// install.sh stamps it into the binary and tests/test-staleness.sh compares it
// with `tandem version --hash`. It imports only internal/sourcehash.
package main

import (
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/sourcehash"
)

func main() {
	root := "."
	switch {
	case len(os.Args) == 2:
		root = os.Args[1]
	case len(os.Args) > 2:
		fmt.Fprintln(os.Stderr, "usage: tandem-source-hash [ROOT]")
		os.Exit(2)
	}
	sum, err := sourcehash.Hash(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tandem-source-hash:", err)
		os.Exit(1)
	}
	fmt.Println(sum)
}
