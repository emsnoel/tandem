package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// supportedKinds names the kinds tandem can run, in the one order every message
// uses.
func supportedKinds() string { return strings.Join(agent.Kinds(), ", ") }

// refuseUnknownKind prints a refusal and returns its exit code when kind has
// no adapter, such as a kind tandem no longer supports. Nothing has been
// created yet when it is called.
func refuseUnknownKind(what, kind string) int {
	_, err := agent.Get(kind)
	if err == nil {
		return exitcode.OK
	}
	fmt.Fprintf(os.Stderr, "tandem build start: %s %q is not supported. Supported kinds: %s\n", what, kind, supportedKinds())
	var ae *agent.Error
	if errors.As(err, &ae) && ae.Exit != 0 {
		return ae.Exit
	}
	return exitcode.NarrowingRefused
}

// kindOrigin says where a resolved kind was set, in the words a person would
// look for it under. manifest names the repository's manifest file.
func kindOrigin(r config.Resolved, manifest string) string {
	switch r.Kind.From {
	case config.FromFlag:
		return "the --" + string(r.Role) + " flag"
	case config.FromEnv:
		return "HERDR_" + strings.ToUpper(string(r.Role)) + "_KIND"
	case config.FromRepo:
		return manifest
	case config.FromUser:
		return config.UserConfigPath()
	}
	return "tandem's defaults"
}

// kindRefusal is the sentence that refuses r's kind. Every tandem-pair path to
// a kind with no adapter ends here.
func kindRefusal(r config.Resolved, manifest string) string {
	return fmt.Sprintf("the %s kind %q from %s is not supported. Use one of the supported kinds: %s",
		r.Role, r.Kind.Value, kindOrigin(r, manifest), supportedKinds())
}

// cmdAgentSupported answers whether tandem can run a kind, with an exit code
// and no output, so a script can word its own refusal.
func cmdAgentSupported(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: tandem agent supported KIND")
		return exitcode.Usage
	}
	if agent.FirstUnsupported(args[0]) != "" {
		return exitcode.NarrowingRefused
	}
	return exitcode.OK
}

// cmdConfigCheckKinds answers whether the agents recorded in a relay can still
// run. A record with no adapter exits NarrowingRefused and prints the role, its
// kind and the supported kinds as NUL fields, for bin/tandem-handoff to explain.
// A relay with no record has nothing to refuse.
func cmdConfigCheckKinds(args []string) int {
	fs := flag.NewFlagSet("config check-kinds", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "", "the relay directory holding builder.json and reviewer.json")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem config check-kinds --relay DIR")
		return exitcode.Usage
	}
	builder, reviewer := "", ""
	if rec, err := config.LoadBuilder(*relay); err == nil {
		builder = rec.Kind
	}
	if rec, err := config.LoadReviewer(*relay); err == nil {
		reviewer = rec.Kind
	}
	for _, r := range []struct{ role, kind string }{{"builder", builder}, {"reviewer", reviewer}} {
		if agent.FirstUnsupported(r.kind) != "" {
			writeFields(os.Stdout, r.role, r.kind, supportedKinds())
			return exitcode.NarrowingRefused
		}
	}
	return exitcode.OK
}
