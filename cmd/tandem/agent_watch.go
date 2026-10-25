package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/mailwatch"
)

// eventWatch is what every wait in tandem reads Herdr's events through
// (herdr.AgentWatch live, a script in tests).
type eventWatch interface {
	Snapshot(name string) *herdr.Agent
	Next(timeout time.Duration) (herdr.Event, bool, error)
	Lost() bool
	Close() error
}

func liveWatch(names ...string) (eventWatch, error) { return herdr.Watch(names...) }

// watchResult is what the Herdr half decided: a line to print, or for an
// error the text for stderr, and the exit code. The caller prints only the
// result that decides, so a watch closed because the mailbox won says nothing.
type watchResult struct {
	line    string
	errText string
	code    int
	decided bool
}

// herdrWait waits for the first status change or departure of a watched
// agent, or for timeout. A broken stream is an error, not a timeout, so the
// bridge paces itself. pane_updated counts when its status differs from the
// last seen, because a moved pane's status subscription names its old id.
func herdrWait(w eventWatch, timeout time.Duration) watchResult {
	deadline := time.Now().Add(timeout)
	last := map[string]string{}
	for {
		ev, ok, err := w.Next(time.Until(deadline))
		if err != nil {
			return watchResult{errText: err.Error(), code: herdr.ExitCodeFor(err), decided: true}
		}
		if !ok {
			return watchResult{}
		}
		if ev.Gone() {
			return watchResult{line: "agent_exited\t" + ev.Name, code: exitcode.AgentExited, decided: true}
		}
		if ev.Status == "" {
			continue
		}
		seen, ok := last[ev.Name]
		if !ok {
			seen = w.Snapshot(ev.Name).StatusString()
		}
		last[ev.Name] = ev.Status
		if ev.Kind == "pane_agent_status_changed" || ev.Status != seen {
			return watchResult{line: "changed\t" + ev.Name + "\t" + ev.Status, code: exitcode.OK, decided: true}
		}
	}
}

// runAgentWatch waits on the Herdr watch w, the mailbox watch m, or both
// (either may be nil), and prints why it returned. The first to decide wins
// and both are closed. A Herdr timeout does not end a wait that has a
// mailbox: the mailbox keeps the same deadline. A mailbox error is a wake,
// so the bridge scans and ticks as it would for a message.
func runAgentWatch(w eventWatch, m mailSource, timeout time.Duration, out io.Writer) int {
	var herdrDone chan watchResult
	if w != nil {
		defer w.Close()
		herdrDone = make(chan watchResult, 1)
		go func() { herdrDone <- herdrWait(w, timeout) }()
	}
	var wakes <-chan mailwatch.Wake
	var errs <-chan error
	if m != nil {
		defer m.Close()
		wakes, errs = m.Wakes(), m.Errors()
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case r := <-herdrDone:
			if r.decided {
				if r.errText != "" {
					fmt.Fprintln(os.Stderr, r.errText)
				}
				if r.line != "" {
					fmt.Fprintln(out, r.line)
				}
				return r.code
			}
			if m == nil {
				fmt.Fprintln(out, "timeout")
				return exitcode.OK
			}
			herdrDone = nil
		case wk := <-wakes:
			fmt.Fprintf(out, "%s\t%s\n", wk.Kind, wk.Path)
			return exitcode.OK
		case err := <-errs:
			fmt.Fprintf(out, "mailbox_error\t%s\n", strings.Join(strings.Fields(err.Error()), " "))
			return exitcode.OK
		case <-t.C:
			fmt.Fprintln(out, "timeout")
			return exitcode.OK
		}
	}
}

// cmdAgentWatch is `tandem agent watch [NAME...] [--root DIR] [--mailbox DIR] --timeout MS`.
func cmdAgentWatch(args []string) int {
	const use = "usage: tandem agent watch [NAME...] [--root DIR] [--mailbox DIR] --timeout MS"
	var names []string
	for len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		names, args = append(names, args[0]), args[1:]
	}
	fs := flag.NewFlagSet("agent watch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "watch the durable run's builder and reviewer")
	mailbox := fs.String("mailbox", "", "also wake on a message in this relay's queues, a change of its run, or a write to the run's state")
	timeoutMS := fs.Int("timeout", 0, "how long to wait, in ms")
	if err := fs.Parse(args); err != nil || *timeoutMS <= 0 {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	var run, stateRoot string
	if *root != "" {
		// Read-only: the bridge runs this between every pass and must not
		// take the writer lease or the tick lock, or leave a nudge.
		d, err := openDurable(*root, os.Getenv("TANDEM_STATE_HOME"), true)
		if err != nil {
			return fail(err)
		}
		ag, err := controller.RunAgents(context.Background(), d.st, d.art, d.run.RunID)
		run, stateRoot = d.run.RunID, d.id.StateRoot
		d.close()
		if err != nil {
			return fail(err)
		}
		names = append(names, ag.BuilderSession)
		if ag.ReviewerSession != "" {
			names = append(names, ag.ReviewerSession)
		}
	}
	if len(names) == 0 && *mailbox == "" {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	var mail mailSource
	if *mailbox != "" {
		mw, ready, err := openMailbox(*mailbox, run, stateRoot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tandem agent watch: the mailbox watch could not start:", err)
			return exitcode.Internal
		}
		if ready != "" {
			mw.Close()
			fmt.Println(ready)
			return exitcode.OK
		}
		mail = mw
	}
	var w eventWatch
	if len(names) > 0 {
		lw, err := liveWatch(names...)
		if err != nil {
			if mail != nil {
				mail.Close()
			}
			fmt.Fprintln(os.Stderr, err.Error())
			var he *herdr.Error
			if errors.As(err, &he) && herdr.IsGone(err) {
				// agent_not_found from a read, or pane_not_found from the
				// subscription when the agent left between its read and it.
				gone := he.Agent
				if gone == "" {
					gone = names[0]
				}
				fmt.Printf("agent_exited\t%s\n", gone)
				return exitcode.AgentExited
			}
			return herdr.ExitCodeFor(err)
		}
		w = lw
	}
	return runAgentWatch(w, mail, time.Duration(*timeoutMS)*time.Millisecond, os.Stdout)
}
