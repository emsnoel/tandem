package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

const explicitPlan = `# Receipts

**Goal:** persist receipts.

## Global Constraints

- No new dependencies.

### Task 1: Event envelope
Task-ID: event-envelope
Paths: internal/controller, internal/workflow
Outputs: envelope-schema

Define the envelope.

### Task 2: Persist the receipt
Task-ID: receipt-store
Depends-On: event-envelope
Paths: internal/state
Outputs: receipt-schema

Store the receipt.

### Task 3: Render
Task-ID: render
Depends-On: event-envelope, receipt-store

Render it.
`

func specIDs(i Imported) []string {
	var ids []string
	for _, s := range i.Specs {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestImportParsesTheLeadingMetadataBlock(t *testing.T) {
	i := Import(explicitPlan)
	if err := i.Err(); err != nil {
		t.Fatal(err)
	}
	if i.Legacy {
		t.Error("a plan with metadata is not legacy")
	}
	if got := strings.Join(specIDs(i), ","); got != "event-envelope,receipt-store,render" {
		t.Fatalf("ids %s", got)
	}
	s := i.Specs[1]
	if strings.Join(s.DependsOn, ",") != "event-envelope" || strings.Join(s.Paths, ",") != "internal/state" || strings.Join(s.Outputs, ",") != "receipt-schema" {
		t.Errorf("spec %+v", s)
	}
	if got := i.Specs[2].DependsOn; len(got) != 2 || got[0] != "event-envelope" || got[1] != "receipt-store" {
		t.Errorf("depends %v", got)
	}
	if s := i.Specs[2]; s.Paths == nil || s.Outputs == nil || len(s.Paths) != 0 {
		t.Errorf("absent lists must be empty, not nil: %+v", s)
	}
	for _, spec := range i.Specs {
		for _, line := range []string{"Task-ID:", "Depends-On:", "Paths:", "Outputs:"} {
			if strings.Contains(spec.Brief, line) {
				t.Errorf("%s: the brief still carries %q:\n%s", spec.ID, line, spec.Brief)
			}
		}
	}
	if !strings.Contains(i.Specs[1].Brief, "### Task 2: Persist the receipt") || !strings.Contains(i.Specs[1].Brief, "Store the receipt.") {
		t.Errorf("brief lost the heading or the text:\n%s", i.Specs[1].Brief)
	}
	if !strings.Contains(i.Specs[1].Constraints, "No new dependencies.") || !strings.Contains(i.Specs[1].Constraints, "persist receipts") {
		t.Errorf("constraints lost the preamble:\n%s", i.Specs[1].Constraints)
	}
}

func TestPlanMetadataIgnoresCodeFence(t *testing.T) {
	text := "# P\n\n### Task 1: Real\nTask-ID: real\n\nShow the format:\n\n```md\n### Task 2: Example\nTask-ID: example\nDepends-On: nothing\n```\n\n### Task 2: Next\n"
	i := Import(text)
	if err := i.Err(); err != nil {
		t.Fatal(err)
	}
	if len(i.Specs) != 2 || i.Specs[0].ID != "real" {
		t.Fatalf("specs %v", specIDs(i))
	}
	if !strings.Contains(i.Specs[0].Brief, "Task-ID: example") || !strings.Contains(i.Specs[0].Brief, "Depends-On: nothing") {
		t.Errorf("the example was removed from the brief:\n%s", i.Specs[0].Brief)
	}
	if len(i.Specs[0].DependsOn) != 0 {
		t.Errorf("an example in a fence became a dependency: %v", i.Specs[0].DependsOn)
	}
}

func TestPlanMetadataStopsAtTheFirstBlankLine(t *testing.T) {
	text := "# P\n\n### Task 1: A\nTask-ID: a\n\nTask-ID: b\nDepends-On: a\n\n### Task 2: B\nTask-ID: c\n"
	i := Import(text)
	if err := i.Err(); err != nil {
		t.Fatal(err)
	}
	if i.Specs[0].ID != "a" || len(i.Specs[0].DependsOn) != 0 {
		t.Errorf("a field after the blank line was read: %+v", i.Specs[0])
	}
	if !strings.Contains(i.Specs[0].Brief, "Task-ID: b") {
		t.Errorf("text after the blank line must stay in the brief:\n%s", i.Specs[0].Brief)
	}
	// A prose line right under the heading ends the block before any field.
	text = "# P\n\n### Task 1: A\nDo this first.\nTask-ID: a\n"
	i = Import(text)
	if i.Specs[0].ID != "task-1" || !strings.Contains(i.Specs[0].Brief, "Task-ID: a") {
		t.Errorf("a field below prose was read: %+v", i.Specs[0])
	}
}

func TestPlanMetadataRejections(t *testing.T) {
	for name, c := range map[string]struct{ meta, want string }{
		"duplicate field":        {"Task-ID: a\nTask-ID: b", "twice"},
		"duplicate depends":      {"Depends-On: x\nDepends-On: y", "twice"},
		"empty dependency item":  {"Depends-On: a,,b", "empty"},
		"trailing comma":         {"Paths: internal/state,", "empty"},
		"empty value":            {"Outputs:", "empty"},
		"blank path item":        {"Paths: a,   ,b", "empty"},
		"uppercase id":           {"Task-ID: Receipt", "task id"},
		"id with a space":        {"Task-ID: two words", "task id"},
		"id with a comma":        {"Task-ID: a,b", "one task id"},
		"id too long":            {"Task-ID: a" + strings.Repeat("b", 64), "task id"},
		"id starting with digit": {"Task-ID: 1st", "task id"},
		"absolute path":          {"Paths: /etc", "escapes"},
		"parent path":            {"Paths: ../outside", "escapes"},
		"hidden parent":          {"Paths: internal/../../x", "escapes"},
		"near miss field":        {"Task-Id: a", "Task-ID"},
		"depends spelled short":  {"Depends: a", "Depends-On"},
		"path singular":          {"Path: x", "Paths"},
	} {
		i := Import("# P\n\n### Task 1: A\n" + c.meta + "\n\nbody\n")
		err := i.Err()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestPlanReferencesAreChecked(t *testing.T) {
	for name, c := range map[string]struct{ text, want string }{
		"missing predecessor": {"# P\n\n### Task 1: A\nTask-ID: a\nDepends-On: ghost\n\nx\n", `"ghost"`},
		"duplicate id":        {"# P\n\n### Task 1: A\nTask-ID: a\n\nx\n\n### Task 2: B\nTask-ID: a\n\ny\n", "twice"},
		"self dependency":     {"# P\n\n### Task 1: A\nTask-ID: a\nDepends-On: a\n\nx\n", "itself"},
		"cycle":               {"# P\n\n### Task 1: A\nTask-ID: a\nDepends-On: c\n\nx\n\n### Task 2: B\nTask-ID: b\nDepends-On: a\n\ny\n\n### Task 3: C\nTask-ID: c\nDepends-On: b\n\nz\n", "a -> c -> b -> a"},
	} {
		err := Import(c.text).Err()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestPlanReorderKeepsIdentities(t *testing.T) {
	a := Import(explicitPlan)
	// The second and third blocks swap places in the text; "render" now comes
	// before the task it depends on, which is allowed.
	i2, i3 := strings.Index(explicitPlan, "### Task 2"), strings.Index(explicitPlan, "### Task 3")
	b := Import(explicitPlan[:i2] + explicitPlan[i3:] + "\n" + explicitPlan[i2:i3])
	if a.Err() != nil || b.Err() != nil {
		t.Fatal(a.Err(), b.Err())
	}
	if a.PlanHash == b.PlanHash {
		t.Error("an edit must change the plan hash")
	}
	byID := func(i Imported, id string) workflow.TaskSpec {
		for _, s := range i.Specs {
			if s.ID == id {
				return s
			}
		}
		t.Fatalf("no task %s", id)
		return workflow.TaskSpec{}
	}
	for _, id := range []string{"event-envelope", "receipt-store", "render"} {
		if strings.Join(byID(a, id).DependsOn, ",") != strings.Join(byID(b, id).DependsOn, ",") {
			t.Errorf("%s changed dependencies when its place changed", id)
		}
	}
	if specIDs(a)[1] == specIDs(b)[1] {
		t.Error("the order is explicit and separate from identity: position 2 must now be a different task")
	}
}

const legacyPlan = `# Old plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**Goal:** greet.

## Global Constraints

- Never touch main.

### Task 1: First

Do the first.

### Task 2: Second

Do the second.

### Task 3: Third

Do the third.
`

func TestLegacyPlanKeepsPreambleAndChain(t *testing.T) {
	i := Import(legacyPlan)
	if err := i.Err(); err != nil {
		t.Fatal(err)
	}
	if !i.Legacy {
		t.Error("a plan with no metadata is legacy")
	}
	if got := strings.Join(specIDs(i), ","); got != "task-1,task-2,task-3" {
		t.Fatalf("ids %s", got)
	}
	for n, s := range i.Specs {
		if !strings.Contains(s.Constraints, "Never touch main.") || !strings.Contains(s.Constraints, "**Goal:** greet.") {
			t.Errorf("%s lost the preamble:\n%s", s.ID, s.Constraints)
		}
		want := []string{}
		if n > 0 {
			want = []string{i.Specs[n-1].ID}
		}
		if strings.Join(s.DependsOn, ",") != strings.Join(want, ",") {
			t.Errorf("%s depends on %v, want the previous task %v", s.ID, s.DependsOn, want)
		}
		if !strings.Contains(s.Brief, "Do the "+[]string{"first", "second", "third"}[n]) {
			t.Errorf("%s: brief %q", s.ID, s.Brief)
		}
	}
	// The legacy splitter is untouched: the same plan splits the same way.
	p := Split(legacyPlan)
	if len(p.Tasks) != 3 || !strings.Contains(p.Preamble, "Never touch main.") || strings.Contains(p.Preamble, "REQUIRED SUB-SKILL") {
		t.Errorf("Split changed: %+v", p)
	}
}

func TestLegacyShapesStillImport(t *testing.T) {
	// No task headings at all: one task. A plan whose tasks are annotated in
	// only some places mixes generated and declared ids.
	i := Import("Just change teh to the.\n")
	if i.Err() != nil || len(i.Specs) != 1 || i.Specs[0].ID != "task-1" || !i.Legacy {
		t.Errorf("%+v %v", i.Specs, i.Err())
	}
	i = Import("# P\n\n### Task 1: A\nTask-ID: alpha\n\nx\n\n### Task 2: B\n\ny\n")
	if i.Err() != nil || i.Legacy {
		t.Fatal(i.Err())
	}
	if got := strings.Join(specIDs(i), ","); got != "alpha,task-2" {
		t.Errorf("ids %s", got)
	}
	if len(i.Specs[1].DependsOn) != 0 {
		t.Errorf("an annotated plan has explicit dependencies only: %v", i.Specs[1].DependsOn)
	}
}

func TestImportRendersTheBuilderBrief(t *testing.T) {
	i := Import(explicitPlan)
	b := i.Briefs[1]
	for _, want := range []string{"# Receipts", "Task 2 of 3: Persist the receipt", "No new dependencies.", "Store the receipt."} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Task-ID:") || strings.Contains(b, "Depends-On:") {
		t.Errorf("metadata lines reached the builder:\n%s", b)
	}
	if len(i.Titles) != 3 || i.Titles[1] != "Persist the receipt" {
		t.Errorf("titles %v", i.Titles)
	}
}

func TestHashIsTheSHA256OfTheBytes(t *testing.T) {
	sum := sha256.Sum256([]byte(legacyPlan))
	if Hash(legacyPlan) != hex.EncodeToString(sum[:]) || Import(legacyPlan).PlanHash != Hash(legacyPlan) {
		t.Error("the plan hash must be the artifact hash of the raw plan")
	}
	if Hash(legacyPlan) == Hash(legacyPlan+"\n") {
		t.Error("whitespace is part of the plan")
	}
}
