// Package gate contains pure teardown and review decisions: Pre, Post and
// Review only read their input and compute a Verdict. cmd/tandem/gate.go
// gathers the facts.
package gate

import (
	"strings"

	"github.com/emsnoel/tandem/internal/mailbox"
)

type Input struct {
	BuilderPane                 string
	Status, StatusNow           string
	ReportsOK, ReportsPending   []string
	HasCheckCommand, SkipChecks bool
	Force                       bool
	ReportsBefore, ReportsAfter mailbox.Snapshot
	TreeBefore, TreeAfter       string

	// TreeMeasured says the fingerprint was taken, as opposed to empty because git
	// could not be read. Without it a clean tree and an unreadable one look the
	// same, and a clean-to-dirty change would go unseen.
	TreeMeasured bool

	// Durable is the run's own evidence when the relay holds a durable run, and
	// nil otherwise. A durable run writes no handback report, so its sign-off
	// is decided from the run, and the reports directory (which only an
	// earlier legacy build can have filled) is not consulted.
	Durable *DurableRun
}

// DurableRun is what a durable run's teardown is signed off on: each task and
// the acceptance requirements it lacks.
type DurableRun struct {
	RunID, State string
	Tasks        []DurableTask
}

// DurableTask names what one task lacks: "acceptance" when no attempt was
// accepted, otherwise the requirements its accepted attempt no longer meets
// for its final tree. Empty means the task is signed off by its evidence.
type DurableTask struct {
	ID    string
	Unmet []string
}

// Verdict is the outcome of a gate check: Code 0 means "allowed", any other
// value is a specific refusal code from internal/exitcode's 4-9 range, with
// Detail carrying human-readable specifics (e.g. which reports are pending).
type Verdict struct {
	Code   int
	Detail []string
}

// Pre runs the checks that must pass before a teardown's check command runs.
func Pre(in Input) Verdict {
	// No builder pane left to close: bridge/mailbox cleanup needs no sign-off.
	if in.BuilderPane == "" {
		return Verdict{}
	}
	if in.Force {
		return Verdict{}
	}
	if in.Status == "working" || in.Status == "blocked" {
		return Verdict{Code: 9, Detail: []string{in.Status}}
	}
	if in.Durable != nil {
		if v := preDurable(in.Durable); v.Code != 0 {
			return v
		}
	} else if len(in.ReportsOK)+len(in.ReportsPending) == 0 {
		return Verdict{Code: 4}
	} else if len(in.ReportsPending) != 0 {
		// Copy so the Verdict does not alias the caller's slice.
		return Verdict{Code: 5, Detail: append([]string(nil), in.ReportsPending...)}
	}
	if !in.SkipChecks && !in.HasCheckCommand {
		return Verdict{Code: 6}
	}
	return Verdict{}
}

// preDurable is gates 4 and 5 for a durable run: some work was accepted, and
// every task is accepted with its check and review (or their waivers) holding
// for its final tree. Each refused task is one detail line: its id, then what
// it lacks.
func preDurable(d *DurableRun) Verdict {
	accepted := 0
	var lacking []string
	for _, t := range d.Tasks {
		if len(t.Unmet) == 0 || t.Unmet[0] != "acceptance" {
			accepted++
		}
		if len(t.Unmet) != 0 {
			lacking = append(lacking, t.ID+" "+strings.Join(t.Unmet, " "))
		}
	}
	if accepted == 0 {
		return Verdict{Code: 4}
	}
	if len(lacking) != 0 {
		return Verdict{Code: 5, Detail: lacking}
	}
	return Verdict{}
}

// Post runs the checks that must pass after a teardown's check command runs,
// comparing the "before" state captured by Pre against the current state.
func Post(in Input) Verdict {
	if in.BuilderPane == "" {
		return Verdict{}
	}
	if in.Force {
		return Verdict{}
	}
	if changed := in.ReportsBefore.Diff(in.ReportsAfter); len(changed) != 0 {
		return Verdict{Code: 8, Detail: append([]string{"reports"}, changed...)}
	}
	if in.StatusNow == "working" || in.StatusNow == "blocked" {
		return Verdict{Code: 8, Detail: []string{"status", in.StatusNow}}
	}
	if in.TreeMeasured && in.TreeAfter != in.TreeBefore {
		return Verdict{Code: 8, Detail: []string{"tree"}}
	}
	return Verdict{}
}
