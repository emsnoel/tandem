package plan

// The structured import of a plan: stable task ids, declared dependencies and
// the plan's own hash, on top of the legacy split.
//
// A task may carry four optional metadata lines, directly under its heading
// and before the first blank line:
//
//	### Task 2: Persist the receipt
//	Task-ID: receipt-store
//	Depends-On: event-envelope, other-task
//	Paths: internal/state
//	Outputs: receipt-schema
//
// Only those four names, spelled exactly, are metadata, and only there. The
// first line that is not one of them ends the block, so a code example that
// shows the format, or prose that happens to start with "Task-ID:" lower down,
// is never read as one. A line that looks like a misspelt field is an error,
// not silently dropped prose: a lost dependency would reorder the build.
//
// A plan with no metadata at all is a legacy plan. Its tasks get ids
// task-<ordinal>, bound to the plan hash of the run that imported it, and each
// depends on the one before, which is the order the legacy engine ran them in.
// Editing or reordering such a plan changes its hash; nothing here matches
// tasks across two hashes by title or position.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Diagnostic is one reason a plan cannot be imported. Task is the plan
// position of the task it is about, or 0 for the plan as a whole.
type Diagnostic struct {
	Task    int
	Message string
}

func (d Diagnostic) String() string {
	if d.Task == 0 {
		return d.Message
	}
	return fmt.Sprintf("task %d: %s", d.Task, d.Message)
}

// Imported is a plan read as tasks with identities.
type Imported struct {
	// PlanHash is the SHA-256 of the plan's bytes: the artifact hash of the plan.
	PlanHash string
	// Legacy is true when no task carries any metadata.
	Legacy bool
	// Specs are the tasks in plan order. Brief is the task's own text without its
	// metadata lines; Constraints is the plan's whole preamble, for every task.
	Specs  []workflow.TaskSpec
	Titles []string
	// Briefs are the briefs a builder is given: title, preamble and one task,
	// as Plan.Brief renders them, without the metadata lines.
	Briefs      []string
	Diagnostics []Diagnostic
}

// Err is nil when the plan imported cleanly, and otherwise names every problem.
func (i Imported) Err() error {
	if len(i.Diagnostics) == 0 {
		return nil
	}
	var parts []string
	for _, d := range i.Diagnostics {
		parts = append(parts, d.String())
	}
	return errors.New("the plan cannot be imported: " + strings.Join(parts, "; "))
}

// Hash is the SHA-256 of a plan's bytes.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

var nearMiss = regexp.MustCompile(`(?i)^(task|depend|path|output)[A-Za-z-]*\s*:`)

var metaKeys = []string{"Task-ID", "Depends-On", "Paths", "Outputs"}

// Import splits text with Split and reads each task's metadata.
func Import(text string) Imported {
	p := Split(text)
	imp := Imported{PlanHash: Hash(text), Titles: []string{}}
	diag := func(task int, format string, a ...any) {
		imp.Diagnostics = append(imp.Diagnostics, Diagnostic{Task: task, Message: fmt.Sprintf(format, a...)})
	}
	annotated := false
	declared := make([]bool, len(p.Tasks))
	for n, t := range p.Tasks {
		ord := n + 1
		lines := strings.Split(t.Body, "\n")
		spec := workflow.TaskSpec{ID: fmt.Sprintf("task-%d", ord), Constraints: p.Preamble, DependsOn: []string{}, Paths: []string{}, Outputs: []string{}}
		end := t.head + 1
		if t.head >= 0 {
			seen := map[string]bool{}
			// A blank line is not a field, so it ends the block like any prose.
			for ; end < len(lines); end++ {
				key, val, ok := field(lines[end])
				if !ok {
					if nearMiss.MatchString(strings.TrimSpace(lines[end])) {
						diag(ord, "%q looks like a metadata field but is not one; the fields are %s", firstWord(lines[end]), strings.Join(metaKeys, ", "))
					}
					break
				}
				if seen[key] {
					diag(ord, "%s is given twice", key)
					continue
				}
				seen[key] = true
				declared[n] = true
				switch key {
				case "Task-ID":
					if strings.Contains(val, ",") {
						diag(ord, "Task-ID takes one task id, not a list")
					} else if !workflow.ValidTaskID(val) {
						diag(ord, "task id %q is not valid: lowercase letters, digits and hyphens, starting with a letter, at most 64 characters", val)
					} else {
						spec.ID = val
					}
				case "Depends-On":
					spec.DependsOn = list(key, val, ord, diag)
				case "Paths":
					spec.Paths = list(key, val, ord, diag)
				case "Outputs":
					spec.Outputs = list(key, val, ord, diag)
				}
			}
			annotated = annotated || len(seen) > 0
			lines = append(append([]string{}, lines[:t.head+1]...), lines[end:]...)
		}
		body := strings.TrimSpace(strings.Join(lines, "\n"))
		spec.Brief = body
		imp.Specs = append(imp.Specs, spec)
		imp.Titles = append(imp.Titles, t.Title)
		imp.Briefs = append(imp.Briefs, p.brief(n, body))
	}
	imp.Legacy = !annotated
	if imp.Legacy {
		for n := 1; n < len(imp.Specs); n++ {
			imp.Specs[n].DependsOn = []string{imp.Specs[n-1].ID}
		}
	}
	if len(imp.Diagnostics) == 0 {
		if err := workflow.ValidatePlan(imp.Specs); err != nil {
			diag(0, "%s", strings.TrimPrefix(err.Error(), workflow.ErrInvalidTransition.Error()+": plan: "))
		}
	}
	return imp
}

// field reads "Name: value" for one of the four metadata names.
func field(line string) (key, val string, ok bool) {
	for _, k := range metaKeys {
		if strings.HasPrefix(line, k+":") {
			return k, strings.TrimSpace(line[len(k)+1:]), true
		}
	}
	return "", "", false
}

func firstWord(line string) string {
	line = strings.TrimSpace(line)
	if i := strings.Index(line, ":"); i >= 0 {
		return line[:i]
	}
	return line
}

// list splits a comma-separated value, refusing an empty list or empty item.
func list(key, val string, task int, diag func(int, string, ...any)) []string {
	if val == "" {
		diag(task, "%s is empty; leave the line out to declare none", key)
		return []string{}
	}
	out := []string{}
	for _, item := range strings.Split(val, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			diag(task, "%s has an empty item", key)
			continue
		}
		out = append(out, item)
	}
	return out
}
