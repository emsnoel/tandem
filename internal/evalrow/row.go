// Package evalrow is the result row a scripted scenario run writes, one JSON
// object per line, and the scenario file that names the runs. The legacy and
// durable engines write the same row, so a token eval kept outside this
// repository reads one shape.
package evalrow

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const SchemaVersion = 1

// Row is one run. Token counts are nil when unknown: zero means "none used",
// never "not measured". A partial total is a lower bound and says so in
// UsageCompleteness and Reason.
type Row struct {
	SchemaVersion     int    `json:"schema_version"`
	Scenario          string `json:"scenario"`
	PairID            string `json:"pair_id"`
	Arm               string `json:"arm"`
	RunID             string `json:"run_id"`
	Adapter           string `json:"adapter"`
	Model             string `json:"model"`
	ConfigHash        string `json:"config_hash"`
	Seed              int    `json:"seed"`
	Mode              string `json:"mode"`
	State             string `json:"state"`
	PauseCode         string `json:"pause_code,omitempty"`
	AcceptedTasks     int    `json:"accepted_tasks"`
	FailedTasks       int    `json:"failed_tasks"`
	PendingTasks      int    `json:"pending_tasks"`
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	UsageCompleteness string `json:"usage_completeness"`
	Reason            string `json:"usage_reason,omitempty"`
	DurationMS        int64  `json:"duration_ms"`
	LatencyBasis      string `json:"latency_basis"`
	Events            int    `json:"events"`
	SourceManifest    string `json:"source_manifest"`
}

// Scenario is one entry of scenarios.json. The driver reads the shape fields;
// Expect is what a correct run must end as.
type Scenario struct {
	ID               string `json:"id"`
	Tasks            int    `json:"tasks"`
	Shape            string `json:"shape"`
	Dirty            bool   `json:"dirty"`
	RedReviews       int    `json:"red_reviews"`
	ContextLimit     int    `json:"context_limit"`
	BulkyConstraints bool   `json:"bulky_constraints"`
	Crash            bool   `json:"crash"`
	PartialUsage     bool   `json:"partial_usage"`
	Expect           Expect `json:"expect"`
}

type Expect struct {
	State     string `json:"state"`
	PauseCode string `json:"pause_code"`
	Accepted  int    `json:"accepted"`
	Failed    int    `json:"failed"`
	Usage     string `json:"usage"`
}

type File struct {
	SchemaVersion int        `json:"schema_version"`
	Adapter       string     `json:"adapter"`
	Model         string     `json:"model"`
	Scenarios     []Scenario `json:"scenarios"`
}

// Load reads and validates a scenario file.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.SchemaVersion != SchemaVersion || len(f.Scenarios) == 0 {
		return File{}, fmt.Errorf("%s: schema version %d with %d scenarios", path, f.SchemaVersion, len(f.Scenarios))
	}
	seen := map[string]bool{}
	for _, s := range f.Scenarios {
		if s.ID == "" || s.Tasks < 1 || seen[s.ID] {
			return File{}, fmt.Errorf("%s: scenario %q is unnamed, repeated or has no tasks", path, s.ID)
		}
		seen[s.ID] = true
	}
	return f, nil
}

// Append adds the row to a JSONL file. A failed run is a row like any other.
func Append(path string, r Row) error {
	r.SchemaVersion = SchemaVersion
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

// Plan is the scenario's plan text: explicit dependencies for the chain and
// sparse shapes, none for independent tasks, and no metadata for a legacy plan.
func (sc Scenario) Plan() string {
	var b strings.Builder
	b.WriteString("# Eval plan\n\n**Goal:** exercise the lifecycle.\n\n")
	if sc.BulkyConstraints {
		b.WriteString("## Global Constraints\n\n")
		for i := 1; i <= 40; i++ {
			fmt.Fprintf(&b, "- Rule %d: every change must keep the build green and the log readable.\n", i)
		}
		b.WriteString("\n")
	}
	for i := 1; i <= sc.Tasks; i++ {
		fmt.Fprintf(&b, "### Task %d: Step %d\n\n", i, i)
		if sc.Shape != "legacy" {
			fmt.Fprintf(&b, "Task-ID: t%d\n", i)
			switch {
			case sc.Shape == "chain" && i > 1:
				fmt.Fprintf(&b, "Depends-On: t%d\n", i-1)
			case sc.Shape == "sparse" && i%4 == 0:
				fmt.Fprintf(&b, "Depends-On: t%d\n", i-1)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Do step %d.\n\n", i)
	}
	return b.String()
}
