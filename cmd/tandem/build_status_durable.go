package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/vcs"
)

type attemptOut struct {
	ID              string `json:"id"`
	Ordinal         int    `json:"ordinal"`
	Current         bool   `json:"current"`
	Implementation  string `json:"implementation"`
	Check           string `json:"check"`
	Review          string `json:"review"`
	Dispatch        string `json:"dispatch,omitempty"`
	ReviewRejection string `json:"review_rejection,omitempty"`
}

type taskOut struct {
	ID       string       `json:"id"`
	Ordinal  int          `json:"ordinal"`
	Accepted bool         `json:"accepted"`
	Attempts []attemptOut `json:"attempts"`
}

type statusDurableOut struct {
	Engine         string    `json:"engine"`
	RunID          string    `json:"run_id"`
	State          string    `json:"state"`
	PauseCode      string    `json:"pause_code,omitempty"`
	PauseReason    string    `json:"pause_reason,omitempty"`
	TaskInputLimit int64     `json:"task_input_limit"`
	RunInputLimit  int64     `json:"run_input_limit"`
	Tasks          []taskOut `json:"tasks"`
}

// durableStatus reads the open run into the object `tandem build status --json`
// prints, so `tandem build wait` reports exactly the same fields.
func durableStatus(d *openRun) (statusDurableOut, error) {
	ctx := context.Background()
	run, err := d.st.GetRun(ctx, d.run.RunID)
	if err != nil {
		return statusDurableOut{}, err
	}
	tasks, err := d.st.RunTasks(ctx, run.ID)
	if err != nil {
		return statusDurableOut{}, err
	}
	out := statusDurableOut{Engine: "durable", RunID: run.ID, State: run.State, PauseCode: run.PauseCode, TaskInputLimit: run.TaskInputLimit, RunInputLimit: run.RunInputLimit, Tasks: []taskOut{}}
	if run.State == "paused" && run.PauseCode == "dispatch_failed" {
		if out.PauseReason, err = dispatchFailure(ctx, d.st, d.art, run.ID); err != nil {
			return statusDurableOut{}, err
		}
	}
	if run.State == "paused" {
		if why := fixedPauseReason(run.PauseCode); why != "" {
			out.PauseReason = why
		}
	}
	effects, err := d.st.RunEffects(ctx, run.ID)
	if err != nil {
		return statusDurableOut{}, err
	}
	rejections, err := d.st.RunReviewRejections(ctx, run.ID)
	if err != nil {
		return statusDurableOut{}, err
	}
	latestRejection := map[string]string{}
	for _, rejection := range rejections {
		latestRejection[rejection.AttemptID] = rejection.Reason
	}
	pauseRejection := ""
	if run.State == "paused" && run.PauseCode == "review_unknown" && run.PauseJobID != "" {
		for _, rejection := range rejections {
			if rejection.JobID == run.PauseJobID {
				pauseRejection = rejection.Reason
			}
		}
	}
	for _, t := range tasks {
		to := taskOut{ID: t.ID, Ordinal: t.Ordinal, Accepted: t.AcceptedAttemptID != "", Attempts: []attemptOut{}}
		attempts, err := d.st.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return statusDurableOut{}, err
		}
		for _, a := range attempts {
			disp, err := dispatchOf(ctx, d.st, effects, a.ID)
			if err != nil {
				return statusDurableOut{}, err
			}
			rejection := ""
			if a.Current {
				rejection = latestRejection[a.ID]
				if run.PauseJobID == "" && rejection != "" {
					pauseRejection = rejection
				}
			}
			to.Attempts = append(to.Attempts, attemptOut{ID: a.ID, Ordinal: a.Ordinal, Current: a.Current, Implementation: string(a.Implementation),
				Check: string(a.Check), Review: string(a.Review), Dispatch: disp, ReviewRejection: rejection})
		}
		out.Tasks = append(out.Tasks, to)
	}
	if run.State == "paused" && run.PauseCode == "review_unknown" && pauseRejection != "" {
		out.PauseReason += ": " + pauseRejection
	}
	return out, nil
}

func cmdBuildStatusDurable(args []string) int {
	fs := flag.NewFlagSet("build status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	asJSON := fs.Bool("json", false, "print the status as one JSON object")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if f.root == "" {
		f.root = "."
		if top, err := vcs.Toplevel("."); err == nil {
			f.root = top
		}
	}
	d, err := openDurable(f.root, f.home, true)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	out, err := durableStatus(d)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return printJSON(out)
	}
	fmt.Printf("engine: durable\nrun:    %s\nstate:  %s", out.RunID, out.State)
	if out.PauseCode != "" {
		fmt.Printf(" (%s)", out.PauseCode)
	}
	if out.PauseReason != "" {
		fmt.Printf("\nreason: %s", out.PauseReason)
	}
	fmt.Printf("\n\n| Task | Accepted | Attempt | Implementation | Check | Review |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, t := range out.Tasks {
		if len(t.Attempts) == 0 {
			fmt.Printf("| %s | %v | - | - | - | - |\n", t.ID, t.Accepted)
		}
		for _, a := range t.Attempts {
			mark := ""
			if a.Current {
				mark = "*"
			}
			fmt.Printf("| %s | %v | %d%s | %s | %s | %s |\n", t.ID, t.Accepted, a.Ordinal, mark, a.Implementation, a.Check, a.Review)
		}
	}
	for _, task := range out.Tasks {
		for _, attempt := range task.Attempts {
			if attempt.ReviewRejection != "" {
				fmt.Printf("\nReview handback for %s attempt %d: %s\n", task.ID, attempt.Ordinal, attempt.ReviewRejection)
			}
		}
	}
	return exitcode.OK
}

// maxReviewResult bounds a review result read from a file or a pipe.
const maxReviewResult = 1 << 20

// cmdBuildReviewRecord is the explicit receipt of a pane review: the reviewer's
// one JSON object, bound to the review job, attempt, patch and tree it was given.
// It records the result and never accepts the task; the next tick does, from
// the attempt's persisted evidence. The controller reads a pane reply itself
