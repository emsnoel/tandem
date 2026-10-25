package taskcontext

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/workflow"
)

type selection struct {
	ID       string `json:"id"`
	Reason   string `json:"reason"`
	Required bool   `json:"required"`
}

type omission struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type manifest struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	AttemptID     string `json:"attempt_id"`
	PlanHash      string `json:"plan_hash"`
	StartSnapshot string `json:"start_snapshot_hash"`
	TaskID        string `json:"task_id"`
	Method        string `json:"estimate_method"`
	Limit         int    `json:"limit"`
	Estimated     struct {
		Required int `json:"required"`
		Optional int `json:"optional"`
		Total    int `json:"total"`
	} `json:"estimated"`
	Sections   []Section   `json:"sections"`
	Selected   []selection `json:"selected"`
	Omitted    []omission  `json:"omitted"`
	PromptHash string      `json:"prompt_hash"`
	ConfigHash string      `json:"config_hash"`
}

func validate(req Request) error {
	switch {
	case !idPattern.MatchString(req.RunID) || !idPattern.MatchString(req.AttemptID):
		return invalid("run and attempt ids must be 32 lowercase hex characters")
	case !hashPattern.MatchString(req.PlanHash) || !hashPattern.MatchString(req.StartSnapshotHash):
		return invalid("plan and start snapshot hashes must be 64 lowercase hex characters")
	case !workflow.ValidTaskID(req.Task.ID):
		return invalid("%q is not a task id", req.Task.ID)
	case strings.TrimSpace(req.Task.Brief) == "":
		return invalid("task %s has no brief", req.Task.ID)
	}
	for name, s := range map[string]string{"header": req.Header, "footer": req.Footer, "brief": req.Task.Brief, "constraints": req.Task.Constraints} {
		if !utf8.ValidString(s) {
			return invalid("the %s is not valid UTF-8; mandatory text is never altered, so it is refused", name)
		}
	}
	for _, e := range req.Entries {
		if !utf8.ValidString(e.Value) {
			return invalid("the value of memory entry %s is not valid UTF-8", e.ID)
		}
	}
	return nil
}

// paragraph ends s with exactly one blank line.
func paragraph(s string) string { return strings.TrimRight(s, "\n") + "\n\n" }

// label says what an entry is and how far to trust it.
func label(e memory.Entry) string {
	if e.Kind == memory.KindBuilderClaim {
		return "builder claim, unverified"
	}
	return strings.ReplaceAll(e.Kind, "_", " ") + ", " + e.Trust
}

// block renders one entry. Every line of its value is indented under it, so a
// value cannot pass for a heading or for another fact.
func block(e memory.Entry) string {
	value := strings.TrimRight(e.Value, "\n")
	return fmt.Sprintf("- %s (%s)\n  %s\n", label(e), e.ID[:12], strings.ReplaceAll(value, "\n", "\n  "))
}
