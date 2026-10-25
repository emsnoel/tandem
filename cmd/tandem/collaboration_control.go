package main

// tandem collaboration stop|resume|amend: a person's audited controls.
//
//	tandem collaboration stop --collaboration ID --actor NAME --reason TEXT [--root DIR] [--state-home DIR]
//	tandem collaboration resume --collaboration ID --actor NAME --reason TEXT [--root DIR] [--state-home DIR]
//	tandem collaboration amend --collaboration ID --provider P [--input-ceiling N] [--output-ceiling N] --actor NAME --reason TEXT [--root DIR] [--state-home DIR]
//
// stop ends launch admission at once; a launch admitted before it is still in
// flight. resume reopens admission, and amend changes a provider's ceilings.
// Both then resume the checkout's durable run if a held launch paused it, so
// the launch is admitted, or held again, on the next tick. Every control is
// recorded with who did it and why.

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

type controlFlags struct {
	durableFlags
	collab, actor, reason string
}

func (f *controlFlags) parse(name string, args []string, extra func(*flag.FlagSet)) bool {
	fs := flag.NewFlagSet("collaboration "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	f.register(fs)
	fs.StringVar(&f.collab, "collaboration", "", "the collaboration id")
	fs.StringVar(&f.actor, "actor", "", "who is doing this")
	fs.StringVar(&f.reason, "reason", "", "why")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || f.collab == "" || f.actor == "" || f.reason == "" {
		fmt.Fprintln(os.Stderr, collaborationUse)
		return false
	}
	return true
}

func cmdCollaborationStop(args []string) int {
	var f controlFlags
	if !f.parse("stop", args, nil) {
		return exitcode.Usage
	}
	st, _, err := openCheckoutState(f.durableFlags)
	if err != nil {
		return collabFail(err)
	}
	defer st.Close()
	if err := st.StopCollaboration(context.Background(), state.ControlRequest{CollaborationID: f.collab, Actor: f.actor, Reason: f.reason}); err != nil {
		return collabFail(err)
	}
	return printJSON(map[string]string{"collaboration_id": f.collab, "admission": "stopped"})
}

func cmdCollaborationResume(args []string) int {
	var f controlFlags
	if !f.parse("resume", args, nil) {
		return exitcode.Usage
	}
	st, _, err := openCheckoutState(f.durableFlags)
	if err != nil {
		return collabFail(err)
	}
	if err := st.ResumeCollaboration(context.Background(), state.ControlRequest{CollaborationID: f.collab, Actor: f.actor, Reason: f.reason}); err != nil {
		st.Close()
		return collabFail(err)
	}
	st.Close()
	return printJSON(map[string]any{"collaboration_id": f.collab, "admission": "open", "run_resumed": resumeHeldRun(f.durableFlags)})
}

func cmdCollaborationAmend(args []string) int {
	var f controlFlags
	var provider string
	var in, out int64
	if !f.parse("amend", args, func(fs *flag.FlagSet) {
		fs.StringVar(&provider, "provider", "", "the provider whose ceilings change")
		fs.Int64Var(&in, "input-ceiling", -1, "the new input ceiling")
		fs.Int64Var(&out, "output-ceiling", -1, "the new output ceiling")
	}) || provider == "" || (in < 0 && out < 0) {
		fmt.Fprintln(os.Stderr, "tandem collaboration amend: name --provider and at least one non-negative ceiling")
		return exitcode.Usage
	}
	req := state.AmendRequest{CollaborationID: f.collab, Provider: provider, Actor: f.actor, Reason: f.reason}
	if in >= 0 {
		req.InputCeiling = &in
	}
	if out >= 0 {
		req.OutputCeiling = &out
	}
	st, _, err := openCheckoutState(f.durableFlags)
	if err != nil {
		return collabFail(err)
	}
	if err := st.AmendBudget(context.Background(), req); err != nil {
		st.Close()
		return collabFail(err)
	}
	st.Close()
	return printJSON(map[string]any{"collaboration_id": f.collab, "provider": provider, "run_resumed": resumeHeldRun(f.durableFlags)})
}

// resumeHeldRun resumes the checkout's durable run when a held launch paused
// it. No run, or a run paused for another reason, is left as it is.
func resumeHeldRun(f durableFlags) bool {
	if f.root == "" {
		return false
	}
	d, err := openDurable(f.root, f.home, false)
	if err != nil {
		return false
	}
	defer d.close()
	c, err := d.controller(noSend{})
	if err != nil {
		return false
	}
	return c.ResumeHeld(context.Background(), d.run.RunID) == nil
}
