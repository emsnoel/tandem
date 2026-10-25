package controller

// The bounded-v1 recovery policy: which failures the controller may repair
// without asking, and what it does with the rest.
//
// A run opts in at start with a policy file pinned by hash. The policy can only
// narrow bounded recovery: it cannot add failure kinds, raise the
// one-repair-per-work limit or enable replanning, and a looser file is refused
// at start.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	maxRecoveryPolicyBytes = 8 << 10

	kindDeclaredCheck  = "declared_local_check"
	kindReviewChanges  = "review_changes_requested"
	kindStuck          = "stuck"
	kindReviewFailed   = "review_failed"
	kindUnknown        = "unknown"
	recoveryPolicyName = "bounded-v1"
)

// automaticKinds is the whole allowlist. Infra errors, missing prerequisites,
// external service failures, uncertain deliveries and anything unclassified are
// not in it, and no policy file can put them there.
var automaticKinds = map[string]bool{kindDeclaredCheck: true, kindReviewChanges: true}

// RecoveryPolicy is a parsed bounded-v1 policy.
type RecoveryPolicy struct {
	SchemaVersion   int    `json:"schema_version"`
	Name            string `json:"name"`
	AutomaticRepair struct {
		Kinds []string `json:"kinds"`
	} `json:"automatic_repair"`
	MaxPerWork int    `json:"max_automatic_repairs_per_work"`
	EscalateTo string `json:"escalate_to"`
	Replanning string `json:"replanning"`
}

// ParseRecoveryPolicy reads a policy strictly and refuses anything looser than
// bounded-v1.
func ParseRecoveryPolicy(raw []byte) (RecoveryPolicy, error) {
	var p RecoveryPolicy
	if len(raw) > maxRecoveryPolicyBytes {
		return p, fmt.Errorf("recovery policy: larger than %d bytes", maxRecoveryPolicyBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("recovery policy: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return p, errors.New("recovery policy: expected one JSON object and nothing after it")
	}
	switch {
	case p.SchemaVersion != 1:
		return p, fmt.Errorf("recovery policy: schema_version %d; this tandem reads 1", p.SchemaVersion)
	case p.Name != recoveryPolicyName:
		return p, fmt.Errorf("recovery policy: name %q; only %q exists", p.Name, recoveryPolicyName)
	case p.MaxPerWork != 1:
		return p, fmt.Errorf("recovery policy: %d automatic repairs per work; bounded-v1 allows exactly 1", p.MaxPerWork)
	case p.EscalateTo != "planner":
		return p, fmt.Errorf("recovery policy: escalate_to %q; a decision goes to the planner", p.EscalateTo)
	case p.Replanning != "none":
		return p, fmt.Errorf("recovery policy: replanning %q; bounded recovery never replans", p.Replanning)
	case len(p.AutomaticRepair.Kinds) == 0:
		return p, errors.New("recovery policy: automatic_repair.kinds is empty; list the kinds to repair, or leave the policy off")
	}
	seen := map[string]bool{}
	for _, k := range p.AutomaticRepair.Kinds {
		switch {
		case !automaticKinds[k]:
			return p, fmt.Errorf("recovery policy: %q is not a kind bounded-v1 repairs automatically", k)
		case seen[k]:
			return p, fmt.Errorf("recovery policy: kind %q is listed twice", k)
		}
		seen[k] = true
	}
	return p, nil
}

// allows says the policy lists a kind (the allowlist is the ceiling).
func (p RecoveryPolicy) allows(kind string) bool {
	for _, k := range p.AutomaticRepair.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// automaticRepairEligible is the predicate a repair must satisfy before the
// budget and fence checks are even asked: the writer is settled, the failure
// has a concrete target, and its kind is on the allowlist.
func automaticRepairEligible(kind string, writerSettled, concreteTarget bool) bool {
	return writerSettled && concreteTarget && automaticKinds[kind]
}

// loadRecoveryPolicy reads and validates a policy file at start.
func loadRecoveryPolicy(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: recovery policy: %v", workflow.ErrInvalidTransition, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxRecoveryPolicyBytes+1))
	if err != nil {
		return nil, err
	}
	if _, err := ParseRecoveryPolicy(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", workflow.ErrInvalidTransition, err)
	}
	return raw, nil
}

// recoveryPolicy is the run's pinned policy, if it opted in.
func (c *Controller) recoveryPolicy(ctx context.Context, cfg runConfig) (RecoveryPolicy, bool, error) {
	if cfg.RecoveryPolicy == "" {
		return RecoveryPolicy{}, false, nil
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, cfg.RecoveryPolicy, maxRecoveryPolicyBytes+1)
	if err != nil {
		return RecoveryPolicy{}, false, err
	}
	p, err := ParseRecoveryPolicy(raw)
	return p, err == nil, err
}
