package evidence

import (
	"strings"
	"testing"
)

func TestDigestChangesWithThePlan(t *testing.T) {
	a := Build("plan one", true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	b := Build("plan two", true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	if a.Digest == b.Digest {
		t.Fatal("two different plans must not share a digest")
	}
}

// The digest binds the contract and policy versions too, so a retune
// invalidates cached advice rather than silently reusing it.
func TestDigestChangesWithThePolicyVersion(t *testing.T) {
	a := Build("plan", true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	b := Build("plan", true, "claude", "auto", "contract/v1", "policy/v2", "jev-1.13.0")
	if a.Digest == b.Digest {
		t.Fatal("a policy change must produce a different digest")
	}
}

func TestDigestIsStableForIdenticalInput(t *testing.T) {
	a := Build("plan", true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	b := Build("plan", true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	if a.Digest != b.Digest {
		t.Fatal("identical input must produce an identical digest")
	}
}

func TestOversizedPlanIsTruncatedAndSaysSo(t *testing.T) {
	big := strings.Repeat("x", MaxPlanBytes+100)
	s := Build(big, true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	if !s.Truncated {
		t.Fatal("an oversized plan must be marked truncated")
	}
	if len(s.Plan) > MaxPlanBytes+len(truncationMarker) {
		t.Fatalf("plan was not actually truncated: %d bytes", len(s.Plan))
	}
	if !strings.Contains(s.Plan, truncationMarker) {
		t.Fatal("a truncated plan must carry its marker so the model is not misled")
	}
}

func TestPlanWithinBudgetIsUntouched(t *testing.T) {
	s := Build("short plan", true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	if s.Truncated || s.Plan != "short plan" {
		t.Fatalf("a small plan must pass through unchanged, got %q truncated=%v", s.Plan, s.Truncated)
	}
}
