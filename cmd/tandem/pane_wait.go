package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
)

// shellWaitInterval is how often wait-shell looks at the pane again.
const shellWaitInterval = 250 * time.Millisecond

// shellReady reports whether the pane's foreground process is its shell, which
// is when `agent start` accepts it.
func shellReady(info processInfo) bool {
	return info.Foreground != nil && info.Shell != nil && *info.Foreground == *info.Shell
}

// waitShell polls read until the shell is in the foreground or timeout passes.
// A failed read counts as not ready yet. On a timeout it returns the error of
// the last read, nil when that read worked and the shell was just busy.
func waitShell(read func() (processInfo, error), timeout time.Duration, now func() time.Time, sleep func(time.Duration)) (bool, error) {
	deadline := now().Add(timeout)
	for {
		info, err := read()
		if err == nil && shellReady(info) {
			return true, nil
		}
		if !now().Before(deadline) {
			return false, err
		}
		sleep(shellWaitInterval)
	}
}

// cmdPaneWaitShell is `tandem pane wait-shell PANE --timeout MS`.
func cmdPaneWaitShell(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: tandem pane wait-shell PANE --timeout MS")
		return exitcode.Usage
	}
	if len(args) != 3 || args[1] != "--timeout" {
		return usage()
	}
	ms, err := strconv.Atoi(args[2])
	if err != nil || ms < 0 {
		return usage()
	}
	pane := args[0]
	read := func() (processInfo, error) {
		b, err := herdr.Raw("pane", "process-info", "--pane", pane)
		if err != nil {
			return processInfo{}, err
		}
		return decodeProcessInfo(b)
	}
	ok, err := waitShell(read, time.Duration(ms)*time.Millisecond, time.Now, time.Sleep)
	if ok {
		return exitcode.OK
	}
	var he *herdr.Error
	if errors.As(err, &he) && he.Unreachable {
		fmt.Fprintln(os.Stderr, "tandem pane wait-shell:", err)
		return herdr.ExitCodeFor(err)
	}
	return exitcode.ManualGate
}
