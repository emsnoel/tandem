package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// cmdBuildStartDurable starts a durable run. retireLegacy, when set, is the
// directory of a finished legacy build: it is archived and verified, then
// removed from the relay, before the run is created.
func cmdBuildStartDurable(args []string, retireLegacy string) int {
	fs := flag.NewFlagSet("build start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	planPath := fs.String("plan", "", "absolute path of the plan")
	builder := fs.String("builder", "builder", "builder agent name")
	planner := fs.String("planner", "planner", "planner agent name")
	kind := fs.String("builder-kind", "", "the builder's agent kind (codex, claude, copilot, opencode)")
	reviewer := fs.String("reviewer", "", "reviewer agent name: every review prompt goes to this pane (needs --reviewer-kind; default: the pane tandem-pair recorded in reviewer.json)")
	reviewerKind := fs.String("reviewer-kind", "", "the reviewer pane's agent kind (codex, claude, copilot, opencode; needs --reviewer)")
	check := fs.String("check", "", "the project's check command")
	budget := fs.Int64("budget", 0, "input tokens one task may use; 0 is off")
	runBudget := fs.Int64("run-budget", 0, "input tokens the whole run may use; 0 is off")
	share := fs.Bool("share-context", false, "send a task to a builder with no verified reset in the conversation it already has")
	ctxLimit := fs.Int("context-limit", 0, "most bytes of prompt a builder is given for one task; 0 is the default of 24000")
	collab := fs.String("collaboration", "", "attach the run to an open collaboration (see tandem collaboration create)")
	delivery := fs.String("review-delivery", "", "how reviews come back: manual (default) or mailbox (direct handback; needs --collaboration and --acceptance-policy)")
	recoveryPath := fs.String("recovery-policy", "", "opt in to bounded recovery: one automatic repair per piece of work, then one decision (needs --collaboration)")
	policyPath := fs.String("acceptance-policy", "", "the two-stage acceptance policy file a mailbox run is pinned to")
	_ = fs.Bool("judge", false, "accepted and ignored: durable runs always review in the pane")
	resumeOnly := fs.Bool("resume-only", false, "refuse unless a durable run exists to continue")
	dry := fs.Bool("dry-run", false, "decide and print, write nothing")
	if err := fs.Parse(args); err != nil || *planPath == "" || f.root == "" || *ctxLimit < 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem build start --plan FILE --root DIR --builder-kind KIND [--builder N] [--planner N] [--reviewer N --reviewer-kind KIND] [--check CMD] [--budget N] [--run-budget N] [--context-limit N] [--share-context] [--collaboration ID] [--review-delivery manual|mailbox] [--acceptance-policy FILE] [--recovery-policy FILE] [--state-home DIR] [--resume-only] [--dry-run]\n"+
			"A run needs a reviewer pane: the one tandem-pair recorded in reviewer.json, or --reviewer with --reviewer-kind.")
		return exitcode.Usage
	}
	if err := validateReviewDelivery(*delivery, *collab, *policyPath); err != nil {
		fmt.Fprintln(os.Stderr, "tandem build start:", err)
		return exitcode.Usage
	}
	if *recoveryPath != "" && *collab == "" {
		fmt.Fprintln(os.Stderr, "tandem build start: --recovery-policy needs --collaboration: the repair allowance belongs to a collaboration's budget groups")
		return exitcode.Usage
	}
	relay := paths.Relay(f.root)
	if run, err := controller.ReadActiveRun(relay); err != nil {
		return fail(err)
	} else if run != nil {
		if *resumeOnly && *dry {
			return printJSON(map[string]any{"engine": "durable", "dry_run": true, "run_id": run.RunID})
		}
		if *resumeOnly {
			return cmdBuildTick([]string{"--root", f.root, "--state-home", f.home}, true)
		}
		return fail(fmt.Errorf("a durable run (%s) is already active in %s; `tandem build recover` continues it, `tandem build stop` ends it", run.RunID, relay))
	} else if *resumeOnly {
		return fail(errors.New("no durable run to continue"))
	}
	if *kind == "" {
		fmt.Fprintln(os.Stderr, "tandem build start: a durable run needs --builder-kind")
		return exitcode.Usage
	}
	req := controller.StartRequest{Root: f.root, PlanPath: *planPath, BuilderKind: *kind, BuilderSession: *builder,
		PlannerSession: *planner, CheckCommand: *check, TaskInputLimit: *budget, RunInputLimit: *runBudget, ContextLimit: *ctxLimit, ShareContext: *share,
		Collaboration: *collab, ReviewDelivery: *delivery, ReviewProtocol: reviewProtocolFor(*delivery), AcceptancePolicyPath: *policyPath, RecoveryPolicyPath: *recoveryPath,
		ReviewerKind: *reviewerKind, ReviewerSession: *reviewer}
	// The flags override the recorded pane. Without them the record decides,
	// and a missing or bad record needs a person to re-pair, not a new command.
	var err error
	if req.ReviewerKind, req.ReviewerSession, err = controller.RecordedReviewerPane(relay, req); err != nil {
		fmt.Fprintln(os.Stderr, "tandem build start:", err)
		if *reviewer != "" || *reviewerKind != "" {
			return exitcode.Usage
		}
		return exitcode.ManualGate
	}
	// Every task's first prompt is measured before anything exists, so a task
	// too large for the limit is refused here instead of pausing a new run.
	if err := controller.CheckTaskContext(req); errors.Is(err, workflow.ErrContextTooLarge) {
		fmt.Fprintln(os.Stderr, "tandem build:", err)
		return exitcode.ManualGate
	} else if err != nil {
		return fail(err)
	}
	if *dry {
		out := map[string]any{"engine": "durable", "dry_run": true}
		if retireLegacy != "" {
			out["archives_legacy"] = retireLegacy
		}
		return printJSON(out)
	}
	// The kind is checked before anything is created.
	spec := transportSpec{Kind: *kind, Builder: *builder, Planner: *planner, Root: f.root, ShareContext: *share, ReviewerKind: req.ReviewerKind, Reviewer: req.ReviewerSession}
	if _, err := durableTransport(spec); err != nil {
		return fail(err)
	}
	id, err := state.ResolveIdentity(f.root, f.home)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	st, err := state.Open(ctx, id.StateRoot)
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		return fail(err)
	}
	spec.Artifacts = art
	tr, err := durableTransport(spec)
	if err != nil {
		return fail(err)
	}
	archived := ""
	if retireLegacy != "" {
		// The same verified copy a new legacy start makes of the build it
		// replaces. Only once it is verified does the relay copy go.
		res, err := build.Archive(f.root, retireLegacy)
		if err != nil {
			return fail(fmt.Errorf("archiving the finished legacy build before the durable run: %w", err))
		}
		if err := os.RemoveAll(retireLegacy); err != nil {
			return fail(err)
		}
		archived = res.Dir
	}
	c, err := controller.New(controller.Dependencies{State: st, Artifacts: art, Transport: tr, CheckExecutor: controller.NewCheckExecutor(art), Owner: cliOwner})
	if err != nil {
		return fail(err)
	}
	runID, err := c.Start(ctx, req)
	if err != nil {
		if runID != "" {
			if code, ok := gatedRun(ctx, st, runID); ok {
				fmt.Fprintln(os.Stderr, "tandem build:", err)
				printJSON(tickOut{RunID: runID, State: "paused", PauseCode: code})
				return exitcode.ManualGate
			}
			fmt.Fprintf(os.Stderr, "tandem build: run %s was created but could not finish starting: %v; `tandem build recover` continues it\n", runID, err)
			return exitcode.Internal
		}
		return fail(err)
	}
	out := map[string]any{"engine": "durable", "run_id": runID}
	if archived != "" {
		out["archived_legacy"] = archived
	}
	return printJSON(out)
}

func cmdBuildStopDurable(args []string) int {
	fs := flag.NewFlagSet("build stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	// The legacy flags are accepted so a caller written for them still works.
	_ = fs.String("reason", "", "ignored by a durable run")
	_ = fs.String("detail", "", "ignored by a durable run")
	_ = fs.Int("task", 0, "ignored by a durable run")
	if err := fs.Parse(args); err != nil || f.root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem build stop --root DIR [--state-home DIR]")
		return exitcode.Usage
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	c, err := d.controller(noSend{})
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	if err := c.Stop(ctx, d.run.RunID); err != nil {
		return fail(err)
	}
	run, err := d.st.GetRun(ctx, d.run.RunID)
	if err != nil {
		return fail(err)
	}
	return printJSON(tickOut{RunID: run.ID, State: run.State, PauseCode: run.PauseCode})
}

// gateCodes are the pause codes that need a person: the controller refused to
// go on rather than guess, and said why.
var gateCodes = map[string]bool{"plan_changed": true, "context_too_large": true, "memory_conflict": true, "context_incomplete": true}

// gatedRun reports whether the run is paused at one of those gates.
func gatedRun(ctx context.Context, st *state.Store, runID string) (string, bool) {
	run, err := st.GetRun(ctx, runID)
	if err != nil || run.State != "paused" || !gateCodes[run.PauseCode] {
		return "", false
	}
	return run.PauseCode, true
}
