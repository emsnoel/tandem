package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// MaxReviewRounds is how many review rounds one task gets. A reviewer that
// still asks for changes after the last one stops the build for a person.
const MaxReviewRounds = 3

// Review records the durable state and measured outcome of one review round.
type Review struct {
	RecordID   string `json:"record_id"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason,omitempty"`
	SentAt     string `json:"sent_at"`
	FirstEvent string `json:"first_event"`
	Delivered  bool   `json:"delivered"`
	// Pending marks a review that was due but not sent because the task's
	// budget was spent. `build start` resumes it; review-sent clears it.
	Pending bool `json:"pending,omitempty"`
	// Skipped marks a round the triage gate ended without a reviewer.
	Skipped bool              `json:"skipped,omitempty"`
	Tree    map[string]string `json:"tree,omitempty"`
	Changed *bool             `json:"changed,omitempty"`
	// Reviewed is true once the reviewer pane handed back a verdict for this
	// round, and Result is that verdict: "clean" or "changes".
	Reviewed   *bool    `json:"reviewed,omitempty"`
	Result     string   `json:"result,omitempty"`
	OtherPaths []string `json:"other_paths,omitempty"`
}

// Complete reports whether this round has positive evidence behind it: the
// gate skipped it, or the reviewer pane handed back a clean verdict without
// editing the tree. A round with no verdict, one asking for changes, or one
// whose reviewer edited the tree is never complete.
func (r *Review) Complete() bool {
	if r == nil {
		return false
	}
	edited := r.Changed != nil && *r.Changed
	return r.Skipped || (r.Reviewed != nil && *r.Reviewed && r.Result == "clean" && !edited)
}

// ChangedTree reports whether a reviewer changed a task file or created a new
// file. Changes to paths that were already dirty and outside the task are
// returned separately so shared-tree activity is visible but not attributed.
func (r *Review) ChangedTree(after map[string]string, taskFiles []string) (bool, []string) {
	task := make(map[string]bool, len(taskFiles))
	for _, path := range taskFiles {
		task[path] = true
	}

	changed := false
	other := make([]string, 0)
	seen := make(map[string]bool, len(r.Tree)+len(after))
	for path := range r.Tree {
		seen[path] = true
	}
	for path := range after {
		seen[path] = true
	}
	for path := range seen {
		beforeHash, before := r.Tree[path]
		afterHash, present := after[path]
		if before == present && beforeHash == afterHash {
			continue
		}
		if task[path] || !before {
			changed = true
			continue
		}
		other = append(other, path)
	}
	sort.Strings(other)
	return changed, other
}

// AcceptsEvent rejects the handback that opened the review round and queue
// events that predate the review prompt.
func (r *Review) AcceptsEvent(id, created string) bool {
	if id == r.FirstEvent {
		return false
	}
	if created == "" {
		return true
	}
	sent, err := time.Parse(time.RFC3339, r.SentAt)
	if err != nil {
		return false
	}
	event, err := time.Parse(time.RFC3339, created)
	return err == nil && event.After(sent)
}

// findingsPath is where the reviewer of task is asked to write its findings:
// beside the task's brief, in the relay, which is outside the tracked tree.
func findingsPath(briefPath string, task int) string {
	return filepath.Join(filepath.Dir(briefPath), fmt.Sprintf("task-%d.findings.md", task))
}

// ReviewPrompt is what the reviewer pane named reviewer is sent for one task.
// The reviewer reads the change and the brief, edits nothing, and hands back a
// verdict naming its own pane, since its CLI may not pass HERDR_ROLE.
func ReviewPrompt(task int, title, briefPath, reviewer string) string {
	findings := findingsPath(briefPath, task)
	return fmt.Sprintf(`Tandem asks for a review of task %d (%s).

The brief is at %s. Read it, then run "git diff HEAD" (and "git status" for
new files) to see what the builder changed for this task. Report bugs,
missing cases and anything the brief did not ask for.

Do not edit, create or delete any file in the repository. A review that changes
the tree is not counted. Do not commit.

Write your findings to %s, then hand them back:

    tandem-handback --task %d --review --role %s --verdict clean|changes --report %s

Use --verdict clean when nothing needs fixing, and --verdict changes when the
builder must fix something; the findings then go to the builder as written.
That call returns immediately. Then stop.
`, task, title, briefPath, findings, task, reviewer, findings)
}

// FixPrompt sends a reviewer's findings back to the builder conversation that
// built the task, so it fixes them there and hands the task back again.
func FixPrompt(task int, title, reviewReport string) string {
	return fmt.Sprintf(`The reviewer asked for changes to task %d (%s).

Its findings are in %s. Read them. Fix what is real and leave the rest; say
which you left and why. Do not commit.

Then hand the task back again, as before:

    tandem-handback --task %d --report <file>

The report says what you changed for each finding. Add --stuck if you could
not finish. That call returns immediately. Then stop.
`, task, title, reviewReport, task)
}

// ReviewPromptID identifies the review prompt a reviewer is given: a hash of
// the prompt rendered for fixed inputs, the pane name among them. Any change
// to the prompt's text changes it, so calibration evidence gathered with one
// reviewer prompt is never counted with another's; the pane's name does not.
func ReviewPromptID() string {
	sum := sha256.Sum256([]byte(ReviewPrompt(1, "title", "brief.md", "reviewer")))
	return hex.EncodeToString(sum[:])
}
