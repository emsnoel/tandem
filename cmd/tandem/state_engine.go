package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
)

// cmdStateEngine says which engine owns a relay, and refuses, writing nothing,
// when the relay was written by an engine or protocol this tandem does not
// speak. The shell entrypoints call it before they read or touch anything, so
// an older install cannot drive a newer run into a state it would misread.
func cmdStateEngine(args []string) int {
	fs := flag.NewFlagSet("state engine", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "a directory inside the checkout (default: the current directory)")
	asFields := fs.Bool("fields", false, "print engine and because as NUL-separated fields for bash")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	dir := *root
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return fail(err)
		}
	}
	if top, _, err := state.GitIdentity(dir); err == nil {
		dir = top
	}
	relay := paths.Relay(dir)
	env, repo, err := engineSettings(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem state engine: %v\n", err)
		return exitcode.ManualGate
	}
	engine, why, err := controller.SelectEngineWhy(relay, env, repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem state engine: %v\n  Nothing was changed. If this relay was written by a newer tandem, update this install:\n  re-run install.sh from a current checkout. Otherwise inspect %s by hand.\n", err, relay)
		return exitcode.ManualGate
	}
	if *asFields {
		writeFields(os.Stdout, string(engine), why)
		return exitcode.OK
	}
	return printJSON(map[string]any{"engine": string(engine), "because": why, "relay": relay})
}
