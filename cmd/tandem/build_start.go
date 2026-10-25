package main

// tandem build start: begin or resume a legacy build (see build.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/vcs"
)

// newID is shaped like tandem-handback's report ids, which tandem-unpair
// accepts for --verified.
func newID(now time.Time) string {
	return fmt.Sprintf("%s-%d-%d", now.Format("20060102-150405"), os.Getpid(), rand.Intn(32768))
}

// reportTexts reads each finished task's report, keyed by position. A report
// that cannot be read is left out: the ledger is advice, not evidence.
func reportTexts(b *build.Build) (notes, reviews map[int]string) {
	notes, reviews = map[int]string{}, map[int]string{}
	for i, t := range b.Tasks {
		if raw, err := os.ReadFile(t.Report); t.Report != "" && err == nil {
			notes[i+1] = string(raw)
		}
		if raw, err := os.ReadFile(t.ReviewReport); t.ReviewReport != "" && err == nil {
			reviews[i+1] = string(raw)
		}
	}
	return notes, reviews
}

type startOut struct {
	Task    int    `json:"task"`
	Total   int    `json:"total"`
	Title   string `json:"title"`
	Brief   string `json:"brief"`
	Ledger  string `json:"ledger"`
	Resumed bool   `json:"resumed"`
	Judge   bool   `json:"judge"`
	// Reviewer is the pane every review of this build is typed into.
	Reviewer string `json:"reviewer"`
	// ReviewPending is true when the task is a review that budget paused. The
	// handoff then sends ReviewPrompt to the Reviewer pane instead of starting
	// the task again. The builder never reviews.
	ReviewPending bool   `json:"review_pending,omitempty"`
	ReviewPrompt  string `json:"review_prompt,omitempty"`
	// Replaced names the plan of an unfinished build this start replaced:
	// another plan, or this one before it was edited.
	Replaced string `json:"replaced,omitempty"`
}

func cmdBuildStart(args []string) int {
	fs := flag.NewFlagSet("build start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	planPath := fs.String("plan", "", "absolute path of the plan")
	root := fs.String("root", "", "repository root")
	builder := fs.String("builder", "builder", "builder agent name")
	planner := fs.String("planner", "planner", "planner agent name")
	check := fs.String("check", "", "the project's check command")
	budget := fs.Int64("budget", 0, "input tokens one task may use before the loop stops; 0 is off")
	judge := fs.Bool("judge", false, "let tandem decide when tasks get reviews")
	dry := fs.Bool("dry-run", false, "decide and print, write nothing")
	resumeOnly := fs.Bool("resume-only", false, "refuse unless an unfinished build of this plan exists")
	if err := fs.Parse(args); err != nil || *planPath == "" || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem build start --plan FILE --root DIR [--builder N] [--planner N] [--check CMD] [--budget N] [--judge] [--resume-only] [--dry-run]")
		return exitcode.Usage
	}
	// The builder never reviews, so a build starts only with a reviewer pane
	// to send its reviews to, and one that is neither the builder nor the
	// planner. Refused before anything is read or written.
	_, reviewer, err := controller.RecordedReviewerPane(paths.Relay(*root), controller.StartRequest{BuilderSession: *builder, PlannerSession: *planner})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem build: %v. The build was not started.\n", err)
		return exitcode.ManualGate
	}
	raw, err := os.ReadFile(*planPath)
	if err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(raw)
	now := time.Now()
	dir := build.Dir(paths.Relay(*root))
	if *resumeOnly {
		// Decided before Start, which would otherwise replace another plan's
		// build: continuing must never begin one.
		_, resumable, err := build.Start(dir, plan.Split(string(raw)), *planPath, hex.EncodeToString(sum[:]), build.Options{}, true)
		if err != nil {
			return fail(err)
		}
		if !resumable {
			fmt.Fprintf(os.Stderr, "tandem build: no unfinished build of %s to continue (the plan may have changed since the build began)\n", *planPath)
			return exitcode.Internal
		}
	}
	old, err := build.Load(dir)
	if err != nil {
		return fail(err)
	}
	b, resumed, err := build.Start(dir, plan.Split(string(raw)), *planPath, hex.EncodeToString(sum[:]), build.Options{
		ID: newID(now), Builder: *builder, Reviewer: reviewer, Planner: *planner, Check: *check, Budget: *budget, Judge: *judge,
		Now: now.UTC().Format(time.RFC3339), HistoryRoot: *root,
	}, *dry)
	if err != nil {
		return fail(err)
	}
	t := b.Task(b.Current)
	reviewPending := resumed && t.Status == build.StatusReviewing && t.Review != nil && t.Review.Pending
	reviewPrompt := ""
	if reviewPending {
		// The implementation is done and stays as it is. Only the review round
		// begins, from the tree as it is now.
		reviewPrompt = filepath.Join(dir, fmt.Sprintf("review-%d.md", b.Current))
		if !*dry {
			tree, err := vcs.DirtyHashes(*root)
			if err != nil {
				return fail(fmt.Errorf("reading the tree: %w", err))
			}
			t.Review.Tree, t.Review.SentAt = tree, now.UTC().Format(time.RFC3339)
			if reviewPrompt, err = writeReviewPrompt(dir, b.Reviewer, b.Current, t); err != nil {
				return fail(err)
			}
			if err := b.Save(dir); err != nil {
				return fail(err)
			}
		}
	} else if !*dry {
		// Taken before the task is sent, so the files the builder touches are
		// told apart from ones that were already dirty.
		tree, err := vcs.DirtyHashes(*root)
		if err != nil {
			return fail(fmt.Errorf("reading the tree: %w", err))
		}
		t.TreeBefore = tree
		if err := b.Save(dir); err != nil {
			return fail(err)
		}
	}
	replaced := ""
	if old != nil && !resumed && old.State != build.StateFinished {
		replaced = old.Plan
	}
	notes, reviews := reportTexts(b)
	return printJSON(startOut{
		Task: b.Current, Total: len(b.Tasks), Title: t.Title, Brief: t.Brief,
		Ledger: build.Ledger(b, notes, reviews), Resumed: resumed, Replaced: replaced, Judge: b.Judge, Reviewer: b.Reviewer,
		ReviewPending: reviewPending, ReviewPrompt: reviewPrompt,
	})
}
