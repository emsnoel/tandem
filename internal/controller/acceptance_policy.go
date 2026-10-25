package controller

// The production acceptance policy for a run with direct review handback.
//
// Acceptance needs the declared checks, the reviewer pane's review and the
// planner's independent verification, from a reviewer who is not the writer.
// The policy is a small JSON document naming who performs each stage and what
// evidence each gets. It is stored as an artifact and pinned by hash in the
// run's configuration.
//
// A policy can pin reviewers but never weaken acceptance: anything weaker is
// refused at start, and self-review never satisfies the independent stage.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// stageReview is the reviewer pane's review. Its stored value keeps the old
// name "builder_review", because runs and events already record it.
const (
	stageReview                  = "builder_review"
	stageIndependentVerification = "independent_verification"

	// maxPolicyBytes bounds a policy document.
	maxPolicyBytes = 16 << 10
)

// acceptanceSatisfied is the policy predicate: every gate, and a verifier who
// is not the writer. It is only the predicate. The current tree, the attempt
// and the dependencies are still validated by the reducer's own checks.
func acceptanceSatisfied(checks, builderReview, independentReview, distinctWriter bool) bool {
	return checks && builderReview && independentReview && distinctWriter
}

// StagePolicy says who performs one mandatory stage.
type StagePolicy struct {
	Stage              string `json:"stage"`
	Role               string `json:"role"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	DistinctFromWriter bool   `json:"distinct_from_writer"`
}

// AcceptancePolicy is the parsed policy document.
type AcceptancePolicy struct {
	SchemaVersion int           `json:"schema_version"`
	Stages        []StagePolicy `json:"stages"`
}

// Stage returns the policy of one stage.
func (p AcceptancePolicy) Stage(name string) (StagePolicy, bool) {
	for _, s := range p.Stages {
		if s.Stage == name {
			return s, true
		}
	}
	return StagePolicy{}, false
}

// policyDocument is the wire form. Booleans are pointers so a missing field is
// told apart from false, which would otherwise read as a weakening that was
// never written.
type policyDocument struct {
	SchemaVersion int `json:"schema_version"`
	Stages        []struct {
		Stage              string `json:"stage"`
		Role               string `json:"role"`
		Provider           string `json:"provider"`
		Model              string `json:"model"`
		DistinctFromWriter *bool  `json:"distinct_from_writer"`
	} `json:"stages"`
	Checks *struct {
		Required *bool `json:"required"`
	} `json:"checks"`
	Evidence *struct {
		TaskBrief       *bool `json:"task_brief"`
		Constraints     *bool `json:"constraints"`
		DependencyFacts *bool `json:"dependency_facts"`
		CheckResults    *bool `json:"check_results"`
		PatchAndTree    *bool `json:"patch_and_tree"`
	} `json:"evidence"`
}

// requiredRoles is who may perform each stage.
var requiredRoles = map[string]string{stageReview: "reviewer", stageIndependentVerification: "planner"}

// ParseAcceptancePolicy reads and validates a policy document.
func ParseAcceptancePolicy(raw []byte) (AcceptancePolicy, error) {
	bad := func(format string, a ...any) (AcceptancePolicy, error) {
		return AcceptancePolicy{}, fmt.Errorf("acceptance policy: "+format, a...)
	}
	if len(raw) > maxPolicyBytes {
		return bad("%d bytes, over %d", len(raw), maxPolicyBytes)
	}
	var doc policyDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return bad("not a policy document: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return bad("data after the document")
	}
	if doc.SchemaVersion != 1 {
		return bad("schema_version %d; this tandem reads 1", doc.SchemaVersion)
	}
	out := AcceptancePolicy{SchemaVersion: 1}
	seen := map[string]bool{}
	for _, s := range doc.Stages {
		role, known := requiredRoles[s.Stage]
		switch {
		case !known:
			return bad("%q is not a review stage", s.Stage)
		case seen[s.Stage]:
			return bad("stage %s appears twice", s.Stage)
		case s.Role != role:
			return bad("stage %s is performed by the %s, not %q", s.Stage, role, s.Role)
		case s.Provider == "" || s.Model == "":
			return bad("stage %s names no provider and model", s.Stage)
		case s.DistinctFromWriter == nil:
			return bad("stage %s does not say whether its reviewer must be distinct from the writer", s.Stage)
		case s.Stage == stageIndependentVerification && !*s.DistinctFromWriter:
			return bad("independent verification must be distinct from the writer: a self-review is not independent")
		}
		seen[s.Stage] = true
		out.Stages = append(out.Stages, StagePolicy{Stage: s.Stage, Role: s.Role, Provider: s.Provider, Model: s.Model, DistinctFromWriter: *s.DistinctFromWriter})
	}
	for stage := range requiredRoles {
		if !seen[stage] {
			return bad("stage %s is missing: both stages are mandatory", stage)
		}
	}
	if doc.Checks == nil || doc.Checks.Required == nil || !*doc.Checks.Required {
		return bad("the declared checks must be required")
	}
	if doc.Evidence == nil {
		return bad("the evidence requirements are missing")
	}
	for name, v := range map[string]*bool{"task_brief": doc.Evidence.TaskBrief, "constraints": doc.Evidence.Constraints,
		"dependency_facts": doc.Evidence.DependencyFacts, "check_results": doc.Evidence.CheckResults, "patch_and_tree": doc.Evidence.PatchAndTree} {
		if v == nil || !*v {
			return bad("evidence %s must be given to every reviewer", name)
		}
	}
	return out, nil
}

// policy returns the acceptance policy a run is pinned to, or false for a run
// that did not opt in.
func (c *Controller) policy(ctx context.Context, cfg runConfig) (AcceptancePolicy, bool, error) {
	if cfg.AcceptancePolicy == "" {
		return AcceptancePolicy{}, false, nil
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, cfg.AcceptancePolicy, maxPolicyBytes+1)
	if err != nil {
		return AcceptancePolicy{}, false, err
	}
	p, err := ParseAcceptancePolicy(raw)
	return p, err == nil, err
}
