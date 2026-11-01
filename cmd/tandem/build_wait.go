package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// waitInterval is how often a wait looks at the run again.
const waitInterval = 500 * time.Millisecond

// statusView is the subset of `tandem build status --json` the waiters read.
type statusView struct {
	State       string `json:"state"`
	PauseCode   string `json:"pause_code"`
	PauseReason string `json:"pause_reason"`
	Tasks       []struct {
		Accepted bool `json:"accepted"`
		Attempts []struct {
			Current  bool   `json:"current"`
			Dispatch string `json:"dispatch"`
		} `json:"attempts"`
	} `json:"tasks"`
}

// dispatch is the current attempt's dispatch on the first unaccepted task.
func (v statusView) dispatch() string {
	for _, t := range v.Tasks {
		if t.Accepted {
			continue
		}
		for _, a := range t.Attempts {
			if a.Current {
				return a.Dispatch
			}
		}
		return ""
	}
	return ""
}

func (v statusView) matches(until []string, untilNot string) bool {
	if untilNot != "" && v.State != untilNot {
		return true
	}
	for _, u := range until {
		if u == "sent" {
			if v.dispatch() == "sent" {
				return true
			}
		} else if v.State == u {
			return true
		}
	}
	return false
}

// waitForRun checks the run every waitInterval until its state is one of
// until, the current attempt was sent (for "sent"), or its state is not
// untilNot (when set), or the timeout passes. It returns the last view it read
// and whether that view matched. A failed read counts as not matched yet; its
// error is returned only if the last read before the deadline also failed. The
// last check happens at the deadline.
func waitForRun(read func() (statusView, error), tick func() error, until []string, untilNot string, timeout time.Duration, now func() time.Time, sleep func(time.Duration)) (statusView, bool, error) {
	deadline := now().Add(timeout)
	var last statusView
	for {
		if tick != nil {
			_ = tick()
		}
		v, err := read()
		if err == nil {
			last = v
			if v.matches(until, untilNot) {
				return v, true, nil
			}
		}
		if !now().Before(deadline) {
			return last, false, err
		}
		sleep(waitInterval)
	}
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

// cmdBuildWait is `tandem build wait`: it prints the run's status object and
// exits 0 once the run reaches a wanted state, 23 at the timeout.
func cmdBuildWait(args []string) int {
	fs := flag.NewFlagSet("build wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	var until listFlag
	fs.Var(&until, "until", "a state to wait for, or sent (repeatable)")
	timeoutMS := fs.Int("timeout", 0, "milliseconds to wait")
	untilNot := fs.String("until-not", "", "return once the run's state is not this one")
	tick := fs.Bool("tick", false, "run a tick before each check")
	asFields := fs.Bool("fields", false, "print NUL-separated fields for bash (state, pause_code, dispatch, pause_reason) in place of JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || f.root == "" || (len(until) == 0 && *untilNot == "") || *timeoutMS < 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem build wait --root DIR --until STATE... [--until-not STATE] --timeout MS [--tick] [--fields] [--state-home DIR]")
		return exitcode.Usage
	}
	var last statusDurableOut
	read := func() (statusView, error) {
		d, err := openDurable(f.root, f.home, true)
		if err != nil {
			return statusView{}, err
		}
		defer d.close()
		if last, err = durableStatus(d); err != nil {
			return statusView{}, err
		}
		raw, err := json.Marshal(last)
		if err != nil {
			return statusView{}, err
		}
		var v statusView
		return v, json.Unmarshal(raw, &v)
	}
	var tk func() error
	if *tick {
		seen := map[string]bool{}
		tk = func() error {
			_, _, err := tickRun(f, false, false)
			if err != nil && !seen[err.Error()] {
				seen[err.Error()] = true
				fmt.Fprintln(os.Stderr, "tandem build wait: tick:", err)
			}
			return err
		}
	}
	v, ok, err := waitForRun(read, tk, until, *untilNot, time.Duration(*timeoutMS)*time.Millisecond, time.Now, time.Sleep)
	if err != nil {
		return fail(err)
	}
	if *asFields {
		// Printed on a timeout too: the caller names where the run is stuck.
		writeFields(os.Stdout, last.State, last.PauseCode, v.dispatch(), last.PauseReason)
	} else if rc := printJSON(last); rc != exitcode.OK {
		return rc
	}
	if !ok {
		return exitcode.ManualGate
	}
	return exitcode.OK
}
