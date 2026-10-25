package triage

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

func TestAuditSelectionIndependentOfVerdict(t *testing.T) {
	// The choice depends on the job and the policy and on nothing the judge says.
	selected := 0
	for i := 0; i < 1000; i++ {
		job := fmt.Sprintf("run-%d-t1", i)
		a := AuditSelected(job, "policy-A")
		for j := 0; j < 5; j++ {
			if AuditSelected(job, "policy-A") != a {
				t.Fatalf("%s: the choice changed between calls", job)
			}
		}
		if a {
			selected++
		}
	}
	// About a fifth, deterministically.
	if selected < 150 || selected > 250 {
		t.Errorf("%d of 1000 selected", selected)
	}
	// Another policy draws a different sample from the same jobs.
	diff := 0
	for i := 0; i < 1000; i++ {
		job := fmt.Sprintf("run-%d-t1", i)
		if AuditSelected(job, "policy-A") != AuditSelected(job, "policy-B") {
			diff++
		}
	}
	if diff == 0 {
		t.Error("the policy takes no part in the choice")
	}
	// Pin the rule itself: sha256(job + policy) as a number, mod 5, equals 0.
	if got := AuditSelected("job-1", "p"); got != auditByDefinition("job-1", "p") {
		t.Errorf("%v", got)
	}
}

func TestPolicyAndConfigHashesMoveWithWhatTheyCover(t *testing.T) {
	if PolicyHash() == "" || PolicyHash() != PolicyHash() {
		t.Fatal("the policy hash must be stable")
	}
	a := JudgeConfigHash("model-a", "c1", "p1")
	for name, b := range map[string]string{"model": JudgeConfigHash("model-b", "c1", "p1"), "contract": JudgeConfigHash("model-a", "c2", "p1"), "policy": JudgeConfigHash("model-a", "c1", "p2")} {
		if a == b {
			t.Errorf("changing the %s did not change the judge configuration", name)
		}
	}
	if a != JudgeConfigHash("model-a", "c1", "p1") {
		t.Error("not stable")
	}
	r := ReviewerConfigHash("codex", "inherited", "prompt-1")
	for name, other := range map[string]string{"kind": ReviewerConfigHash("claude", "inherited", "prompt-1"), "model": ReviewerConfigHash("codex", "gpt-x", "prompt-1"), "prompt": ReviewerConfigHash("codex", "inherited", "prompt-2")} {
		if r == other {
			t.Errorf("changing the reviewer %s did not change its configuration", name)
		}
	}
	// An unknown part makes the configuration unknown, never a guess.
	for _, parts := range [][3]string{{"", "m", "p"}, {"k", "", "p"}, {"k", "m", ""}} {
		if h := ReviewerConfigHash(parts[0], parts[1], parts[2]); h != "" {
			t.Errorf("%v gave %q", parts, h)
		}
	}
}

func TestEvidenceIdentityIsCompleteOnlyWhenEveryPartIs(t *testing.T) {
	full := EvidenceIdentity{Schema: EvidenceSchema, PolicyHash: "p", JudgeConfigHash: "j", ReviewerConfigHash: "r", InputHash: "i"}
	if !full.Finished() {
		t.Error("a full identity")
	}
	for name, mut := range map[string]func(*EvidenceIdentity){
		"schema":            func(i *EvidenceIdentity) { i.Schema = "" },
		"policy":            func(i *EvidenceIdentity) { i.PolicyHash = "" },
		"judge":             func(i *EvidenceIdentity) { i.JudgeConfigHash = "" },
		"reviewer":          func(i *EvidenceIdentity) { i.ReviewerConfigHash = "" },
		"input":             func(i *EvidenceIdentity) { i.InputHash = "" },
		"schema is another": func(i *EvidenceIdentity) { i.Schema = "something-else" },
	} {
		i := full
		mut(&i)
		if i.Finished() {
			t.Errorf("%s missing but the identity is complete", name)
		}
	}
	if strings.Contains(EvidenceSchema, "staffing") {
		t.Error("the triage schema must be distinct from the staffing policy's")
	}
}

// auditByDefinition is the rule as written: SHA-256 of job and policy read as a
// number, modulo 5, equal to 0.
func auditByDefinition(job, policy string) bool {
	sum := sha256.Sum256([]byte(job + policy))
	return new(big.Int).Mod(new(big.Int).SetBytes(sum[:]), big.NewInt(5)).Sign() == 0
}

func TestPolicyHashMovesWithEveryThreshold(t *testing.T) {
	base := currentThresholds()
	if base.Implements != MinImplements || base.Priority != MaxPriority || base.Rule != SkipRule || base.Capability != CapabilityVersion || base.MaxDiff != MaxDiffBytes {
		t.Fatalf("%+v does not describe the constants", base)
	}
	if hashThresholds(base) != PolicyHash() {
		t.Fatal("PolicyHash is not the hash of the current thresholds")
	}
	for name, mut := range map[string]func(*thresholds){
		"rule":        func(x *thresholds) { x.Rule += "x" },
		"capability":  func(x *thresholds) { x.Capability += "x" },
		"implements":  func(x *thresholds) { x.Implements -= 0.01 },
		"scope":       func(x *thresholds) { x.ScopeExceeded += 0.01 },
		"destructive": func(x *thresholds) { x.Destructive += 0.01 },
		"evidence":    func(x *thresholds) { x.EvidenceSufficient -= 0.01 },
		"priority":    func(x *thresholds) { x.Priority += 0.01 },
		"diff size":   func(x *thresholds) { x.MaxDiff++ },
	} {
		x := base
		mut(&x)
		if hashThresholds(x) == hashThresholds(base) {
			t.Errorf("changing the %s did not change the policy hash, so old evidence would be pooled with new", name)
		}
	}
}
