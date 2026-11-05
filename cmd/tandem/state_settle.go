package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/workflow"
)

// settleActor is who an abandoned delivery is recorded as stated by. Only a
// person at the command line makes this statement.
const settleActor = "owner"

// cmdStateSettleDelivery is an owner giving up on a prompt, which holds whether
// or not the agent read it, and is the one way to close an open delivery without
// its nonce. --abandon is required so the statement is made on purpose.
func cmdStateSettleDelivery(args []string) int {
	fs := flag.NewFlagSet("state settle-delivery", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f stateFlags
	f.register(fs)
	runID := fs.String("run", "", "the run")
	effectID := fs.String("effect", "", "the effect whose delivery is open")
	abandon := fs.Bool("abandon", false, "you are giving up on this prompt")
	because := fs.String("because", "", "why")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *runID == "" || *effectID == "" || !*abandon || strings.TrimSpace(*because) == "" {
		fmt.Fprintln(os.Stderr, stateUse)
		return exitcode.Usage
	}
	id, st, art, err := openCheckout(f)
	if err != nil {
		return refusal("settle-delivery", err)
	}
	defer st.Close()
	ctx := context.Background()
	run, err := st.GetRun(ctx, *runID)
	if err != nil {
		return refusal("settle-delivery", err)
	}
	if run.CheckoutID != id.CheckoutID {
		return refusal("settle-delivery", fmt.Errorf("%w: run %s belongs to another checkout", workflow.ErrConflict, *runID))
	}
	ag, err := controller.RunAgents(ctx, st, art, *runID)
	if err != nil {
		return refusal("settle-delivery", err)
	}
	tr, err := runTransport(ag, id.CanonicalRoot, art)
	if err != nil {
		return refusal("settle-delivery", err)
	}
	c, err := quietController(st, art, tr)
	if err != nil {
		return refusal("settle-delivery", err)
	}
	if err := c.AbandonDelivery(ctx, *runID, *effectID, *because, settleActor); err != nil {
		return refusal("settle-delivery", err)
	}
	return printJSON(map[string]string{"run_id": *runID, "effect_id": *effectID, "state": "cancelled"})
}
