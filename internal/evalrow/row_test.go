package evalrow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRefusesWhatCannotBeRun(t *testing.T) {
	for name, body := range map[string]string{
		"a wrong schema version":  `{"schema_version":2,"scenarios":[{"id":"a","tasks":1}]}`,
		"no scenarios":            `{"schema_version":1,"scenarios":[]}`,
		"a repeated id":           `{"schema_version":1,"scenarios":[{"id":"a","tasks":1},{"id":"a","tasks":1}]}`,
		"an unnamed scenario":     `{"schema_version":1,"scenarios":[{"tasks":1}]}`,
		"a scenario with no work": `{"schema_version":1,"scenarios":[{"id":"a","tasks":0}]}`,
	} {
		if _, err := Load(writeFile(t, body)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestShippedScenariosCoverTheNamedSet(t *testing.T) {
	f, err := Load("testdata/scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range f.Scenarios {
		ids = append(ids, s.ID)
	}
	want := "tiny-2 chain-20 dag-20 shared-dirty red-repair large-required send-crash partial-usage legacy-plan"
	if got := strings.Join(ids, " "); got != want {
		t.Errorf("scenarios %q, want %q", got, want)
	}
}

func TestPlanShapes(t *testing.T) {
	legacy := Scenario{Tasks: 3, Shape: "legacy"}.Plan()
	if strings.Contains(legacy, "Task-ID") || strings.Count(legacy, "### Task ") != 3 {
		t.Errorf("a legacy plan carries no metadata:\n%s", legacy)
	}
	chain := Scenario{Tasks: 3, Shape: "chain"}.Plan()
	if !strings.Contains(chain, "Depends-On: t2") || !strings.Contains(chain, "Depends-On: t1") {
		t.Errorf("a chain depends on its predecessor:\n%s", chain)
	}
	if strings.Contains(Scenario{Tasks: 3, Shape: "independent"}.Plan(), "Depends-On") {
		t.Error("independent tasks depend on nothing")
	}
	sparse := Scenario{Tasks: 9, Shape: "sparse"}.Plan()
	if n := strings.Count(sparse, "Depends-On"); n != 2 || !strings.Contains(sparse, "Depends-On: t3") || !strings.Contains(sparse, "Depends-On: t7") {
		t.Errorf("%d dependencies in a sparse plan of 9, want the 4th and 8th on their predecessors:\n%s", n, sparse)
	}
	if !strings.Contains(Scenario{Tasks: 1, BulkyConstraints: true}.Plan(), "## Global Constraints") {
		t.Error("no constraints section")
	}
}

func TestAppendKeepsUnknownCountsNull(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rows.jsonl")
	in := int64(0)
	if err := Append(p, Row{Scenario: "a", InputTokens: &in}); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, Row{Scenario: "b", FailedTasks: 1}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	var a, b map[string]any
	json.Unmarshal([]byte(lines[0]), &a)
	json.Unmarshal([]byte(lines[1]), &b)
	if a["input_tokens"] != float64(0) || a["schema_version"] != float64(SchemaVersion) {
		t.Errorf("a known zero must stay zero: %v", a)
	}
	if v, ok := b["input_tokens"]; !ok || v != nil {
		t.Errorf("an unknown count must be null, not zero or absent: %v", b)
	}
	if b["failed_tasks"] != float64(1) {
		t.Errorf("a failed run is a row: %v", b)
	}
}
