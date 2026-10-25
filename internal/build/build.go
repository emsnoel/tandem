// Package build keeps the record of a build that runs one plan task per fresh
// builder conversation, and decides what happens when a task ends. Fresh
// conversations keep each task's context small instead of growing with every
// task.
//
// The record lives in <relay>/build/build.json beside one brief per task.
// Tandem fills in what it measures (files changed, the check, token use); the
// builder adds only a short report per task.
//
// Pure apart from reading and writing that directory.
package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/plan"
)

// Task statuses.
const (
	StatusPending   = "pending"
	StatusSent      = "sent"
	StatusReviewing = "reviewing"
	StatusDone      = "done"
	StatusRed       = "red"
	StatusStuck     = "stuck"
)

// Build states.
const (
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateFinished = "finished"
)

// Usage is what one task's conversation cost, from the builder's own log.
type Usage struct {
	Calls int   `json:"calls"`
	Input int64 `json:"input"`
	First int64 `json:"first"`
	Peak  int64 `json:"peak"`
	// Subagents counts the subagent sessions whose calls and input are
	// included above.
	Subagents int `json:"subagents,omitempty"`
}

// Task is one plan task and what happened to it. Tasks are numbered by
// position, from 1.
type Task struct {
	Title        string            `json:"title"`
	Brief        string            `json:"brief"`
	Status       string            `json:"status"`
	Session      string            `json:"session,omitempty"`
	SentAt       string            `json:"sent_at,omitempty"`
	DoneAt       string            `json:"done_at,omitempty"`
	TreeBefore   map[string]string `json:"tree_before,omitempty"`
	Files        []string          `json:"files,omitempty"`
	CheckExit    *int              `json:"check_exit,omitempty"`
	CheckLog     string            `json:"check_log,omitempty"`
	Report       string            `json:"report,omitempty"`
	ReviewReport string            `json:"review_report,omitempty"`
	Usage        *Usage            `json:"usage,omitempty"`
	Review       *Review           `json:"review,omitempty"`
	// ReviewRounds counts the review rounds the reviewer pane handed back a
	// verdict for. MaxReviewRounds caps it.
	ReviewRounds int `json:"review_rounds,omitempty"`
	// Accepted is the planner's reason for marking a stopped task done with
	// `tandem build accept`; AcceptedFrom is the status it stopped with (red
	// or stuck). Both empty for a task tandem itself saw pass.
	Accepted     string `json:"accepted,omitempty"`
	AcceptedFrom string `json:"accepted_from,omitempty"`
}

// Build is the whole record. Current is the position of the task to act on
// next: the one being built, or the one to send when the loop resumes.
type Build struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Plan    string `json:"plan"`
	PlanSHA string `json:"plan_sha"`
	Builder string `json:"builder"`
	// Reviewer is the reviewer pane every review prompt is typed into. The
	// builder never reviews.
	Reviewer   string `json:"reviewer"`
	Planner    string `json:"planner"`
	Check      string `json:"check_command"`
	Budget     int64  `json:"task_input_budget"`
	Judge      bool   `json:"judge"`
	Created    string `json:"created"`
	State      string `json:"state"`
	Current    int    `json:"current"`
	StopReason string `json:"stop_reason,omitempty"`
	StopDetail string `json:"stop_detail,omitempty"`
	StopTask   int    `json:"stop_task,omitempty"`
	Tasks      []Task `json:"tasks"`
}

// Options are the settings a new build is created with.
type Options struct {
	ID       string
	Builder  string
	Reviewer string
	Planner  string
	Check    string
	Budget   int64
	Judge    bool
	Now      string
	// HistoryRoot is the repository root whose .tandem-history keeps a build
	// that a new one replaces. Replacing a build with no place to archive it is
	// an error: the evidence would be lost.
	HistoryRoot string
}

const recordName = "build.json"

// Dir is where a relay keeps its build.
func Dir(relay string) string { return filepath.Join(relay, "build") }

// BriefPath is where task i's brief is written.
func BriefPath(dir string, i int) string { return filepath.Join(dir, fmt.Sprintf("task-%d.md", i)) }

// Load reads the build in dir; (nil, nil) when there is none.
func Load(dir string) (*Build, error) {
	raw, err := os.ReadFile(filepath.Join(dir, recordName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b Build
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, recordName), err)
	}
	return &b, nil
}

// Save writes the record, never leaving a half-written file behind.
func (b *Build) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, recordName+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, recordName))
}

// Task returns the task at position n (from 1), or nil.
func (b *Build) Task(n int) *Task {
	if n < 1 || n > len(b.Tasks) {
		return nil
	}
	return &b.Tasks[n-1]
}

// Start resumes the build in dir when it is for the same plan text and not
// finished, at its first task that is not done. Otherwise it starts a new
// one: the old directory is replaced, a brief is written per task, and task 1
// is current. dry decides without writing. The bool is true on a resume.
func Start(dir string, p plan.Plan, planPath, sha string, o Options, dry bool) (*Build, bool, error) {
	old, err := Load(dir)
	if err != nil {
		return nil, false, err
	}
	if old != nil && old.Plan == planPath && old.PlanSHA == sha && old.State != StateFinished {
		for i, task := range old.Tasks {
			if task.Status != StatusReviewing {
				continue
			}
			// A review that budget paused was never sent, so there is no live
			// reviewer to disturb. It resumes once the budget allows.
			if r := task.Review; r != nil && r.Pending && !r.Delivered {
				if o.Budget > 0 && task.Usage != nil && task.Usage.Input >= o.Budget {
					return nil, false, fmt.Errorf("task %d's review is waiting for budget: it used %d input tokens and the budget is %d; raise the budget to send it", i+1, task.Usage.Input, o.Budget)
				}
				continue
			}
			return nil, false, fmt.Errorf("task %d is in review", i+1)
		}
		old.Current = len(old.Tasks)
		for i, t := range old.Tasks {
			if t.Status != StatusDone {
				old.Current = i + 1
				break
			}
		}
		old.State, old.StopReason, old.StopDetail, old.StopTask = StateRunning, "", "", 0
		// Review ownership is fixed at the first handoff, even on a resume.
		old.Check, old.Budget = o.Check, o.Budget
		// Reviews go to the reviewer pane recorded now: a re-pair may have
		// replaced the one the build started with.
		old.Reviewer = o.Reviewer
		if !dry {
			if err := old.Save(dir); err != nil {
				return nil, false, err
			}
		}
		return old, true, nil
	}
	b := &Build{
		ID: o.ID, Title: p.Title, Plan: planPath, PlanSHA: sha, Builder: o.Builder, Reviewer: o.Reviewer, Planner: o.Planner,
		Check: o.Check, Budget: o.Budget, Judge: o.Judge, Created: o.Now, State: StateRunning, Current: 1,
	}
	for i, t := range p.Tasks {
		b.Tasks = append(b.Tasks, Task{Title: t.Title, Brief: BriefPath(dir, i+1), Status: StatusPending})
	}
	if dry {
		return b, false, nil
	}
	if old != nil {
		if o.HistoryRoot == "" {
			return nil, false, fmt.Errorf("the build in %s would be replaced and no history root was given to archive it", dir)
		}
		if _, err := Archive(o.HistoryRoot, dir); err != nil {
			return nil, false, fmt.Errorf("archiving the build before replacing it: %w", err)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	for i := range p.Tasks {
		if err := os.WriteFile(BriefPath(dir, i+1), []byte(p.Brief(i)), 0o644); err != nil {
			return nil, false, err
		}
	}
	return b, false, b.Save(dir)
}
