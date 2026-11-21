package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/state"
)

const stateResumeAcceptedUse = "usage: tandem state resume-accepted --plan FILE [--from latest|RUN] [--root DIR] [--state-home DIR]"

// cmdStateResumeAccepted prints the comma-separated task IDs already accepted
// by the archived run that a --resume handoff would carry over. Prints nothing
// and exits 0 when no matching archived run or accepted tasks exist.
func cmdStateResumeAccepted(args []string) int {
	fs := flag.NewFlagSet("state resume-accepted", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	planPath := fs.String("plan", "", "path to the plan file")
	from := fs.String("from", controller.ResumeLatest, "archived run to inspect: latest, or a run id")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *planPath == "" {
		fmt.Fprintln(os.Stderr, stateResumeAcceptedUse)
		return exitcode.Usage
	}
	cwd, err := f.cwd()
	if err != nil {
		return fail(err)
	}
	raw, err := os.ReadFile(*planPath)
	if err != nil {
		return fail(err)
	}
	planHash := plan.Hash(string(raw))

	id, bound, err := state.LookupIdentity(cwd, f.home)
	if err != nil {
		return fail(err)
	}
	if !bound {
		return exitcode.OK
	}
	ctx := context.Background()
	s, err := state.OpenReadOnly(ctx, id.StateRoot)
	if errors.Is(err, state.ErrNoStore) {
		return exitcode.OK
	}
	if err != nil {
		return fail(err)
	}
	defer s.Close()

	runID := *from
	if runID == "" || runID == controller.ResumeLatest {
		found, ok, err := s.LatestArchivedRun(ctx, planHash)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return exitcode.OK
		}
		runID = found
	} else {
		r, err := s.GetRun(ctx, runID)
		if err != nil || r.State != "archived" || r.PlanHash != planHash {
			return exitcode.OK
		}
		runID = r.ID
	}

	tasks, err := s.RunTasks(ctx, runID)
	if err != nil {
		return fail(err)
	}
	var accepted []string
	for _, t := range tasks {
		if t.AcceptedAttemptID != "" {
			accepted = append(accepted, t.ID)
		}
	}
	if len(accepted) > 0 {
		fmt.Println(strings.Join(accepted, ","))
	}
	return exitcode.OK
}
