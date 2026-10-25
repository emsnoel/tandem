package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndependentVerificationIsMandatory(t *testing.T) {
	if acceptanceSatisfied(true, true, false, true) {
		t.Fatal("missing independent review accepted")
	}
	if acceptanceSatisfied(true, true, true, false) {
		t.Fatal("writer self-review accepted")
	}
	if !acceptanceSatisfied(true, true, true, true) {
		t.Fatal("all gates rejected")
	}
	for _, missing := range []int{0, 1} {
		gates := [4]bool{true, true, true, true}
		gates[missing] = false
		if acceptanceSatisfied(gates[0], gates[1], gates[2], gates[3]) {
			t.Errorf("accepted with gate %d missing", missing)
		}
	}
}

const goodPolicy = `{
 "schema_version": 1,
 "stages": [
  {"stage": "builder_review", "role": "reviewer", "provider": "codex", "model": "gpt-5.4-codex", "distinct_from_writer": false},
  {"stage": "independent_verification", "role": "planner", "provider": "claude", "model": "claude-opus-5-5", "distinct_from_writer": true}
 ],
 "checks": {"required": true},
 "evidence": {"task_brief": true, "constraints": true, "dependency_facts": true, "check_results": true, "patch_and_tree": true}
}`

func TestAcceptancePolicyParsesBothMandatoryStages(t *testing.T) {
	p, err := ParseAcceptancePolicy([]byte(goodPolicy))
	if err != nil {
		t.Fatal(err)
	}
	iv, ok := p.Stage(stageIndependentVerification)
	if !ok || iv.Role != "planner" || iv.Provider != "claude" || iv.Model != "claude-opus-5-5" || !iv.DistinctFromWriter {
		t.Fatalf("independent verification stage %+v", iv)
	}
	if br, ok := p.Stage(stageReview); !ok || br.Provider != "codex" {
		t.Fatalf("builder review stage %+v", br)
	}
}

// A policy can pin who reviews, but it cannot make acceptance easier: every
// weakening is refused at start, before a run exists.
func TestAcceptancePolicyRefusesEveryWeakening(t *testing.T) {
	edit := func(old, new string) string {
		if !strings.Contains(goodPolicy, old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(goodPolicy, old, new, 1)
	}
	for name, body := range map[string]string{
		"not json":       "policy",
		"another schema": edit(`"schema_version": 1`, `"schema_version": 2`),
		"no independent stage": edit(`,
  {"stage": "independent_verification", "role": "planner", "provider": "claude", "model": "claude-opus-5-5", "distinct_from_writer": true}`, ""),
		"a self-review stage":   edit(`"distinct_from_writer": true`, `"distinct_from_writer": false`),
		"an unknown stage":      edit(`"stage": "builder_review"`, `"stage": "waiver"`),
		"a repeated stage":      edit(`"stage": "builder_review"`, `"stage": "independent_verification"`),
		"checks not required":   edit(`"checks": {"required": true}`, `"checks": {"required": false}`),
		"checks left out":       edit(`"checks": {"required": true},`, ""),
		"no task brief":         edit(`"task_brief": true`, `"task_brief": false`),
		"no constraints":        edit(`"constraints": true`, `"constraints": false`),
		"no dependency facts":   edit(`"dependency_facts": true`, `"dependency_facts": false`),
		"no check results":      edit(`"check_results": true`, `"check_results": false`),
		"no patch and tree":     edit(`"patch_and_tree": true`, `"patch_and_tree": false`),
		"evidence left out":     edit(`"patch_and_tree": true`, `"extra": true`),
		"an unknown field":      edit(`"schema_version": 1,`, `"schema_version": 1, "auto_accept": true,`),
		"no provider":           edit(`"provider": "claude", `, ""),
		"no model":              edit(`"model": "claude-opus-5-5", `, ""),
		"the wrong role":        edit(`"role": "planner"`, `"role": "builder"`),
		"data after the policy": goodPolicy + `{"x":1}`,
		"over the size":         goodPolicy + strings.Repeat(" ", maxPolicyBytes),
		"null":                  "null",
		"an empty object":       "{}",
	} {
		if _, err := ParseAcceptancePolicy([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// policyPath writes the good acceptance policy to a file for a run to start under.
func (e *cenv) policyPath() string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), "acceptance-policy.json")
	if err := os.WriteFile(p, []byte(goodPolicy), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}
