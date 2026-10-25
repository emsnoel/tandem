package main

// tandem collaboration collect|close|report: a collaboration's usage.
//
//	tandem collaboration collect --collaboration ID [--root DIR] [--state-home DIR] [--sessions-dir DIR]
//	tandem collaboration close   --collaboration ID [--root DIR] [--state-home DIR] [--sessions-dir DIR]
//	tandem collaboration report  --collaboration ID --json [--root DIR] [--state-home DIR]
//
// collect reads what is new in every registered source's log and stores a
// snapshot revision if anything changed. close freezes launches first, so no
// paid closeout can start after it, then collects; the collaboration is closed
// only when every source is final, and otherwise stays closing with a partial
// snapshot that says what is missing. report prints the stored current
// snapshot without reading any log. None of them calls a model.

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
)

func usageController(f durableFlags, sessions string) (*controller.Controller, func(), error) {
	st, art, err := openCheckoutState(f)
	if err != nil {
		return nil, nil, err
	}
	logs := controller.DefaultProviderLogs()
	if sessions != "" {
		logs.CodexSessions = sessions
	}
	c, err := controller.New(controller.Dependencies{State: st, Artifacts: art, Transport: noSend{}, CheckExecutor: controller.NewCheckExecutor(art),
		Owner: cliOwner, SessionLogs: logs})
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return c, func() { st.Close() }, nil
}

func cmdCollaborationUsage(verb string, args []string) int {
	fs := flag.NewFlagSet("collaboration "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	collab := fs.String("collaboration", "", "the collaboration id")
	sessions := fs.String("sessions-dir", "", "where Codex files its session logs (default $CODEX_HOME/sessions)")
	asJSON := fs.Bool("json", verb == "report", "print the snapshot as JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *collab == "" {
		fmt.Fprintln(os.Stderr, collaborationUse)
		return exitcode.Usage
	}
	c, closeStore, err := usageController(f, *sessions)
	if err != nil {
		return collabFail(err)
	}
	defer closeStore()
	ctx := context.Background()
	switch verb {
	case "collect":
		err = c.CollectCollaboration(ctx, *collab)
	case "close":
		err = c.CloseCollaboration(ctx, *collab)
	}
	if err != nil {
		return collabFail(err)
	}
	rep, err := c.ReportCollaboration(ctx, *collab)
	if err != nil {
		return collabFail(err)
	}
	if *asJSON {
		return printJSON(rep)
	}
	s := rep.Snapshot
	fmt.Printf("collaboration %s  revision %d  %s\n", *collab, rep.Record.Revision, s.Status)
	if s.Complete {
		fmt.Println("complete: every expected source is final")
	} else {
		fmt.Println("partial: not final")
		for _, r := range s.Reasons {
			fmt.Println("  " + r)
		}
	}
	for provider, t := range s.Totals {
		fmt.Printf("%-8s input %s  cache read %s  cache write %s  output %s%s\n", provider, count(t.InputTotal), count(t.CacheRead),
			count(t.CacheWrite), count(t.Output), map[bool]string{true: "  (lower bound)", false: ""}[t.Partial])
	}
	return exitcode.OK
}

func count(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprint(*v)
}
