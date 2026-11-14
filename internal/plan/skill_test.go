package plan

import (
	"os"
	"strings"
	"testing"
)

// The tandem-plan skill is where a planner learns the plan format. It names
// every metadata field with the spelling Import reads, and its own example
// imports without a diagnostic.
func TestThePlanSkillMatchesTheImporter(t *testing.T) {
	raw, err := os.ReadFile("../../skills/tandem-plan/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := string(raw)
	for _, key := range metaKeys {
		if !strings.Contains(skill, "`"+key+"`") {
			t.Errorf("the skill does not name the %s field", key)
		}
	}
	_, rest, found := strings.Cut(skill, "```\n")
	example, _, closed := strings.Cut(rest, "```")
	if !found || !closed {
		t.Fatal("the skill has no fenced example")
	}
	// The example's dependency is on a task it does not show.
	plan := "# Plan\n\n### Task 1: Define the envelope\nTask-ID: event-envelope\n\nDo it.\n\n" + example + "\nDo it.\n"
	imp := Import(plan)
	if len(imp.Diagnostics) != 0 {
		t.Errorf("the skill's example does not import: %v", imp.Diagnostics)
	}
	if len(imp.Specs) != 2 || imp.Specs[1].ID != "receipt-store" || len(imp.Specs[1].DependsOn) != 1 {
		t.Errorf("the skill's example imported as %+v", imp.Specs)
	}
}
