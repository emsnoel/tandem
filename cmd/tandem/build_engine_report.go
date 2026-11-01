package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/vcs"
	"github.com/emsnoel/tandem/internal/workflow"
)

// scopeList collects repeated --waive flags.
type scopeList []string

func (s *scopeList) String() string     { return strings.Join(*s, ",") }
func (s *scopeList) Set(v string) error { *s = append(*s, v); return nil }

// taskNumber is --task, given as "task-3" or "3".
type taskNumber int

func (n *taskNumber) String() string { return strconv.Itoa(int(*n)) }
func (n *taskNumber) Set(v string) error {
	i, err := strconv.Atoi(strings.TrimPrefix(v, "task-"))
	if err != nil || i < 1 {
		return fmt.Errorf("%q is not a task: give its number or its id, as 3 or task-3", v)
	}
	*n = taskNumber(i)
	return nil
}

// cmdBuildAcceptDurable records the planner's waivers. A durable run has no
// "accept everything": each requirement being overruled is named, with a
// reason, and bound to the tree the planner examined. Acceptance itself still
// happens on the next tick, from persisted evidence.
func cmdBuildAcceptDurable(args []string) int {
	fs := flag.NewFlagSet("build accept", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	var task taskNumber
	fs.Var(&task, "task", "the task whose requirement is waived, as 3 or task-3")
	because := fs.String("because", "", "why the planner overrules it")
	var scopes scopeList
	fs.Var(&scopes, "waive", "check, review or implementation; repeat for each requirement overruled")
	usage := func(msg string) int {
		if msg != "" {
			fmt.Fprintln(os.Stderr, "tandem build accept:", msg)
		}
		fmt.Fprintln(os.Stderr, `usage: tandem build accept [--root DIR] --task N|task-N --waive check|review|implementation [--waive ...] --because "why"`)
		return exitcode.Usage
	}
	if err := fs.Parse(args); err != nil || task < 1 {
		return usage("")
	}
	if len(scopes) == 0 {
		return usage("a durable run is accepted by evidence; to overrule a requirement name it with --waive")
	}
	if strings.TrimSpace(*because) == "" {
		return usage("--because needs a reason")
	}
	for _, s := range scopes {
		if s != "check" && s != "review" && s != "implementation" {
			return usage(fmt.Sprintf("--waive %q is not check, review or implementation", s))
		}
	}
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return usage("not inside a git checkout; pass --root")
		}
		f.root = top
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	taskID, err := controller.ResolveTask(controller.RunDir(paths.Relay(f.root), d.run.RunID), int(task))
	if err != nil {
		return fail(err)
	}
	c, err := d.controller(noSend{})
	if err != nil {
		return fail(err)
	}
	for _, s := range scopes {
		if err := c.Waive(context.Background(), d.run.RunID, taskID, s, *because, "planner"); err != nil {
			return fail(err)
		}
	}
	return printJSON(map[string]any{"run_id": d.run.RunID, "task": taskID, "waived": []string(scopes)})
}

// (controller/review_pane.go); this records one by hand.
func cmdBuildReviewRecord(args []string) int {
	fs := flag.NewFlagSet("build review-record", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	file := fs.String("result", "", "file holding the review result; standard input when empty or -")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem build review-record [--root DIR] [--result FILE] [--state-home DIR]")
		return exitcode.Usage
	}
	var src io.Reader = os.Stdin
	if *file != "" && *file != "-" {
		fh, err := os.Open(*file)
		if err != nil {
			return fail(err)
		}
		defer fh.Close()
		src = fh
	}
	raw, err := io.ReadAll(io.LimitReader(src, maxReviewResult+1))
	if err != nil {
		return fail(err)
	}
	if len(raw) > maxReviewResult {
		return fail(errors.New("review result is larger than 1 MiB"))
	}
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return fail(errors.New("not inside a git checkout; pass --root"))
		}
		f.root = top
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	// The reviewer's pane is observed, never sent to: a result is taken only once
	// its pane has finished.
	ctx := context.Background()
	ag, err := controller.RunAgents(ctx, d.st, d.art, d.run.RunID)
	if err != nil {
		return fail(err)
	}
	tr, err := durableTransport(runTransportSpec(ag, f.root, d.art))
	if err != nil {
		return fail(err)
	}
	c, err := d.controller(tr)
	if err != nil {
		return fail(err)
	}
	if _, err := c.RecordReviewRaw(ctx, d.run.RunID, raw); err != nil {
		return fail(err)
	}
	return printJSON(map[string]any{"run_id": d.run.RunID, "status": "recorded"})
}

// dispatchOf says how an attempt's task prompt got on, from its latest
// builder dispatch: "sent" once the transport accepted it (it holds a receipt,
// or the builder acknowledged it), "pending" while it waits or is retried,
// "failed" when its last attempt failed, "unknown" when it may have been sent,
// and "" when the attempt has none.
func dispatchOf(ctx context.Context, st *state.Store, effects []workflow.Effect, attemptID string) (string, error) {
	for i := len(effects) - 1; i >= 0; i-- {
		e := effects[i]
		if e.AttemptID != attemptID || e.Kind != workflow.EffectBuilderDispatch {
			continue
		}
		switch e.State {
		case "acknowledged":
			return "sent", nil
		case "delivering":
			if _, ok, err := st.EffectReceiptHash(ctx, e.ID); err != nil || ok {
				return "sent", err
			}
			return "pending", nil
		case "delivery_unknown":
			return "unknown", nil
		default: // pending, failed, cancelled
			return e.State, nil
		}
	}
	return "", nil
}

// fixedPauseReason is the reason for a pause whose cause is always the same, or
// "" when the pause needs the run's own record to be explained.
func fixedPauseReason(code string) string {
	switch code {
	case "agent_exited":
		return "The builder or reviewer CLI quit; its pane no longer runs it. Restart it with tandem-pair, then tandem build recover."
	case "no_reviewer":
		return "This run was started before every run needed a reviewer pane, and the builder never reviews. Stop it (tandem build stop), " +
			"end its open jobs (tandem state recover --run ID), archive it (tandem state archive --run ID), " +
			"and re-pair with a reviewer. " + config.NoReviewerRemedy + ". Then hand off again."
	case "review_unknown":
		return "A review prompt was delivered, but no valid review result was found in the reviewer's pane within " + state.AckWindow.String() + ". " +
			"Read its reply (herdr agent read NAME --source recent-unwrapped --lines 120), save the whole JSON object to a file, " +
			"then run tandem build review-record --result FILE. Or overrule it with tandem build accept --task N --waive review --because WHY."
	}
	return ""
}

// dispatchFailure is why the run's last failed effect could not be sent, or ""
// when none kept a reason. A pane whose turn ended at once gave what it
// showed, so the planner can see a rejected model without reading the pane.
func dispatchFailure(ctx context.Context, st *state.Store, art *artifact.Store, runID string) (string, error) {
	effects, err := st.RunEffects(ctx, runID)
	if err != nil {
		return "", err
	}
	for i := len(effects) - 1; i >= 0; i-- {
		if effects[i].State != "failed" {
			continue
		}
		hash, ok, err := st.EffectNotSentReason(ctx, effects[i].ID)
		if err != nil || !ok {
			return "", err
		}
		b, err := art.ReadAll(ctx, hash, 1<<20)
		if err != nil {
			return "", err
		}
		var r struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return "", err
		}
		return r.Error, nil
	}
	return "", nil
}
