package main

// tandem build: the commands that start and drive a run. bin/tandem-handoff
// calls `start` and bin/tandem-bridge ticks the run.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
)

const buildUse = "usage: tandem build <start|accept|stop|status|event|tick|wait|recover|review-record|reconcile|usage|budget> --root DIR ...\n       tandem build pause-codes"

func cmdBuild(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, buildUse)
		return exitcode.Usage
	}
	if args[0] == "start" {
		if rest, ok := withoutFlag(args[1:], "--fields"); ok {
			return cmdBuildStartFields(rest)
		}
	}
	if rc, handled := runCommand(args[0], args[1:]); handled {
		return rc
	}
	switch args[0] {
	case "start":
		return cmdBuildStartDurable(args[1:])
	case "event":
		return cmdBuildEvent(args[1:])
	case "pause-codes":
		writeFields(os.Stdout, controller.PauseCodes()...)
		return exitcode.OK
	}
	fmt.Fprintln(os.Stderr, buildUse)
	return exitcode.Usage
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "tandem build:", err)
	return exitcode.Internal
}

func printJSON(v any) int {
	var w io.Writer = os.Stdout
	if jsonOut != nil {
		w = jsonOut
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fail(err)
	}
	return exitcode.OK
}
