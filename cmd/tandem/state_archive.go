package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/workflow"
)

// cmdStateArchive makes a finished or paused run history, keeps all of it, and
// sets the relay's marker aside so a later build starts clean. tandem-unpair
// calls it before it removes the relay.
func cmdStateArchive(args []string) int {
	fs := flag.NewFlagSet("state archive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	runID := fs.String("run", "", "the run to archive")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *runID == "" {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	id, st, art, err := openCheckout(f)
	if err != nil {
		return refusal("archive", err)
	}
	defer st.Close()
	ctx := context.Background()
	run, err := st.GetRun(ctx, *runID)
	if err != nil {
		return refusal("archive", err)
	}
	if run.CheckoutID != id.CheckoutID {
		return refusal("archive", fmt.Errorf("%w: run %s belongs to another checkout", workflow.ErrConflict, *runID))
	}
	if err := st.ArchiveRun(ctx, *runID); err != nil {
		return refusal("archive", err)
	}
	// An archive is a collection point for the run's collaboration. The
	// snapshot stays provisional: closing the collaboration is separate. A
	// failed collection is reported and leaves the archive in place.
	collected := "none"
	if c, err := controller.New(controller.Dependencies{State: st, Artifacts: art, Transport: noSend{}, CheckExecutor: controller.NewCheckExecutor(art),
		Owner: cliOwner, SessionLogs: controller.DefaultProviderLogs()}); err != nil {
		collected = "failed: " + err.Error()
	} else if err := c.CollectRun(ctx, *runID); err != nil {
		collected = "failed: " + err.Error()
	} else {
		collected = "collected"
	}
	marker := "none"
	relay := paths.Relay(id.CanonicalRoot)
	if ar, err := controller.ReadActiveRun(relay); err == nil && ar != nil && ar.RunID == *runID {
		// Named by the run, so archiving one run never overwrites the marker an
		// earlier archive set aside.
		aside := filepath.Join(relay, "active-run."+*runID+".archived.json")
		if err := os.Rename(filepath.Join(relay, controller.ActiveRunFile), aside); err != nil {
			return refusal("archive", fmt.Errorf("the run is archived but its marker could not be set aside: %w", err))
		}
		marker = "archived"
	}
	return printJSON(map[string]any{"run_id": *runID, "state": "archived", "marker": marker, "collaboration_usage": collected})
}
