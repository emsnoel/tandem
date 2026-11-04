package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// kindOrder is the order the supported kinds are named in a refusal. A kind
// registered later follows these, sorted.
var kindOrder = []string{"codex", "claude", "copilot"}

// supportedKinds is agent.Kinds() in the order a person expects to read it.
func supportedKinds() []string {
	kinds := agent.Kinds()
	rank := func(k string) int {
		if i := slices.Index(kindOrder, k); i >= 0 {
			return i
		}
		return len(kindOrder)
	}
	slices.SortStableFunc(kinds, func(a, b string) int { return rank(a) - rank(b) })
	return kinds
}

// refuseUnknownKind prints a refusal and returns its exit code when kind has
// no adapter, such as a kind tandem no longer supports. Nothing has been
// created yet when it is called.
func refuseUnknownKind(what, kind string) int {
	_, err := agent.Get(kind)
	if err == nil {
		return exitcode.OK
	}
	fmt.Fprintf(os.Stderr, "tandem build start: %s %q is not supported. Supported kinds: %s\n", what, kind, strings.Join(supportedKinds(), ", "))
	var ae *agent.Error
	if errors.As(err, &ae) && ae.Exit != 0 {
		return ae.Exit
	}
	return exitcode.NarrowingRefused
}
