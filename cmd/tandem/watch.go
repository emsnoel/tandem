package main

// The waits on Herdr's event stream, and what each does when the stream
// fails. "Stream error" is Next returning an error (the socket closed or sent
// something unreadable), "Lost" is an event dropped because the reader fell
// behind, "timeout" is the deadline passing with no deciding event, and
// "closing read fails" is the one `agent get` after any of those failing.
//
//	wait           stream error                    Lost                    timeout                closing read fails
//	awaitStatus    sleep out the deadline, then    closing read at once    closing read           want("idle"), error as readErr:
//	               the closing read (the dialog                                                   before a start the caller
//	               pre-wait passes a no-op sleep:                                                 reports it (exit 3 when
//	               nothing is typed yet)                                                          unreachable); after a start,
//	                                                                                              the turn did not keep running
//	awaitSession   sleep out the deadline, then    closing read at once    closing read           "" (no new session)
//	               the closing read
//	agentAlive     sleep out the window, then      closing read at once    closing read           gone (agent_not_found or
//	               the closing read; a watch                                                      pane_not_found): not alive;
//	               that cannot open does the                                                      any other error: alive
//	               same unless it failed as gone
//	runReset       stop waiting: "unconfirmed",    not checked: every      a read each            press the confirm key once
//	               no closing read                 event and poll slice    resetPoll;             and keep waiting
//	                                               is followed by a read   "unconfirmed" at the
//	                                                                       deadline
//	build_advance  as awaitStatus, per slice; a    as awaitStatus          next slice, until      not settled this slice; the
//	               watch that cannot open sleeps                           settleTimeout stops    next slice tries again
//	               the slice and tries again                               the task "unsettled"
//	runAgentWatch  print it and exit with its      not checked: the        print "timeout",       none: it ends on events only
//	               code (3 when unreachable), so   newest event is never   exit 0
//	               the bridge paces itself         the one dropped

import (
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

// awaitStatus waits up to within for name's status to satisfy want, on w's
// events. A release counts as settled: the agent stopped. A wait ends on its
// event or at its deadline, with one closing read. A broken stream waits out
// the rest of the deadline first, with one sleep: a prompt that has not shown
// yet may still land. Dropped events (Lost) read at once. read is the agent
// the closing read saw, nil when the wait ended on an event; readErr is set
// when the closing read failed, and the agent then counts as gone.
func awaitStatus(get func(string) (*herdr.Agent, error), now func() time.Time, sleep func(time.Duration), w eventWatch, within time.Duration, want func(string) bool, name string) (matched bool, read *herdr.Agent, readErr error) {
	deadline := now().Add(within)
	closing := func() (bool, *herdr.Agent, error) {
		a, err := get(name)
		if err != nil {
			return want("idle"), nil, err
		}
		return want(a.StatusString()), a, nil
	}
	for {
		ev, ok, err := w.Next(deadline.Sub(now()))
		if err != nil {
			if rest := deadline.Sub(now()); rest > 0 {
				sleep(rest)
			}
			return closing()
		}
		if !ok || w.Lost() {
			return closing()
		}
		if ev.Name != name {
			continue
		}
		if ev.Gone() {
			return want("idle"), nil, nil
		}
		if ev.Status != "" && want(ev.Status) {
			return true, nil, nil
		}
	}
}

// awaitSession waits for name's reported session to be one other than before.
// It follows awaitStatus's rule: it ends on the session event, a broken stream
// waits out the rest of the deadline with one sleep, and dropped events (Lost)
// or the deadline read at once. One closing read answers; "" means no new
// session, never before.
func awaitSession(d herdrDeps, w eventWatch, name, before string, within time.Duration) string {
	fresh := func(s string) bool { return s != "" && s != before }
	if a := w.Snapshot(name); fresh(a.SessionID()) {
		return a.SessionID()
	}
	deadline := d.now().Add(within)
	closing := func() string {
		if a, err := d.get(name); err == nil && fresh(a.SessionID()) {
			return a.SessionID()
		}
		return ""
	}
	for {
		ev, ok, err := w.Next(deadline.Sub(d.now()))
		if err != nil {
			if rest := deadline.Sub(d.now()); rest > 0 {
				d.sleep(rest)
			}
			return closing()
		}
		if !ok || w.Lost() {
			return closing()
		}
		if ev.Name == name && fresh(ev.Session) {
			return ev.Session
		}
	}
}

// agentAlive watches an agent that was just started, for within. It counts as
// running unless Herdr reports it released or its pane closed, or its pane is
// back at the shell at the deadline. For a gone agent it returns why and the
// end of its pane, read by pane id. A broken stream waits out the window, then
// does the closing read.
func agentAlive(d herdrDeps, name string, within time.Duration) (alive bool, why, paneText string) {
	const quit = "Herdr no longer lists it: its CLI quit"
	w, err := d.watch(name)
	if err != nil {
		if herdr.IsGone(err) {
			return false, quit, tailOfPane(d, "")
		}
		d.sleep(within)
		return closingAliveRead(d, name, "")
	}
	defer w.Close()
	pane := ""
	if a := w.Snapshot(name); a != nil {
		pane = a.PaneID
	}
	deadline := d.now().Add(within)
	for {
		ev, ok, err := w.Next(deadline.Sub(d.now()))
		if err != nil {
			if rest := deadline.Sub(d.now()); rest > 0 {
				d.sleep(rest)
			}
			return closingAliveRead(d, name, pane)
		}
		if !ok || w.Lost() {
			return closingAliveRead(d, name, pane)
		}
		if ev.Name != name {
			continue
		}
		if ev.PaneID != "" {
			pane = ev.PaneID
		}
		if ev.Gone() {
			return false, "its CLI quit (Herdr released it)", tailOfPane(d, pane)
		}
	}
}

// closingAliveRead is agentAlive's one read at the end of the window.
func closingAliveRead(d herdrDeps, name, pane string) (bool, string, string) {
	a, err := d.get(name)
	if err != nil {
		if herdr.IsGone(err) {
			return false, "Herdr no longer lists it: its CLI quit", tailOfPane(d, pane)
		}
		return true, "", ""
	}
	pane = a.PaneID
	if shell, err := d.shellForeground(pane); err == nil && shell {
		return false, "its pane is back at the shell: its CLI quit", tailOfPane(d, pane)
	}
	return true, "", ""
}
