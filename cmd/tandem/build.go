package main

// tandem build / tandem check: the Go side of a build that runs one plan task
// per fresh builder conversation (see internal/build).
//
// bin/tandem-handoff calls `start` before it sends a task and `sent` once the
// builder has it. bin/tandem-bridge calls `advance` at handback and `stop` when
// the next task cannot be sent. `status` and `check` are for people and the
// builder.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/vcs"
)

const buildUse = "usage: tandem build <start|sent|review-sent|advance|accept|stop|status|archive|event|tick|recover|review-record|usage|budget> --root DIR ..."

func cmdBuild(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, buildUse)
		return exitcode.Usage
	}
	if rc, handled := routeBuild(args[0], args[1:]); handled {
		return rc
	}
	switch args[0] {
	case "start":
		return cmdBuildStart(args[1:])
	case "sent":
		return cmdBuildSent(args[1:])
	case "review-sent":
		return cmdBuildReviewSent(args[1:])
	case "advance":
		return cmdBuildAdvance(liveAdvanceDeps, args[1:])
	case "accept":
		return cmdBuildAccept(args[1:])
	case "stop":
		return cmdBuildStop(args[1:])
	case "status":
		return cmdBuildStatus(args[1:])
	case "archive":
		return cmdBuildArchive(args[1:])
	case "event":
		return cmdBuildEvent(args[1:])
	}
	fmt.Fprintln(os.Stderr, buildUse)
	return exitcode.Usage
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "tandem build:", err)
	return exitcode.Internal
}

func loadBuild(root string) (string, *build.Build, error) {
	dir := build.Dir(paths.Relay(root))
	b, err := build.Load(dir)
	if err == nil && b == nil {
		err = fmt.Errorf("no build in %s; tandem-handoff starts one", dir)
	}
	return dir, b, err
}

func printJSON(v any) int {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		return fail(err)
	}
	return exitcode.OK
}

// cmdCheck runs the current build's check command quietly: full output to a
// log under the build, a short summary on stdout, and the check's own exit
// code. Arguments after -- are appended to the command.
func cmdCheck(args []string) int {
	var extra []string
	for i, a := range args {
		if a == "--" {
			args, extra = args[:i], args[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root (default: this git checkout)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "usage: tandem check [--root DIR] [-- ARGS...]")
		return exitcode.Usage
	}
	r := *root
	if r == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return fail(fmt.Errorf("not inside a git checkout; pass --root"))
		}
		r = top
	}
	dir, b, err := loadBuild(r)
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(b.Check) == "" {
		fmt.Fprintln(os.Stderr, "tandem check: this project has no check command; set check_command in .tandem.json")
		return exitcode.Usage
	}
	log := filepath.Join(dir, "checks", time.Now().Format("20060102-150405")+".log")
	res, text, err := runCheckLogged(r, b.Check, extra, log, checkTimeout)
	if err != nil {
		return fail(err)
	}
	fmt.Print(summary(res, text))
	if res.Exit > 255 || res.Exit < 0 {
		return 1
	}
	return res.Exit
}
