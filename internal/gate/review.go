package gate

import "strings"

// ReviewInput carries everything Review needs to judge a stored plan review.
//
// Every field is gathered by cmd/tandem and handed in; this package reads no
// files and runs no subprocesses. See the package comment on teardown.go.
type ReviewInput struct {
	// Found says a review naming this plan exists at all.
	Found bool

	// PlanNow is the plan file's current text; PlanReviewed is the verbatim
	// copy stored when the review was made.
	PlanNow, PlanReviewed string
	// PlanMeasured says both texts were read. Unreadable files are both empty, and
	// empty equals empty.
	PlanMeasured bool

	// The builder's identity when the review was made, and now. Class-4
	// findings ("I cannot do this here") are properties of this builder's
	// authority, not of the plan, so they do not survive a change of either.
	KindNow, KindReviewed         string
	AutonomyNow, AutonomyReviewed string
	IdentityMeasured              bool

	// A plan review belongs to the reviewer, bound to who wrote it and to the
	// reviewer's kind and model at the time. Effort is not an input.
	//
	// ReviewerRecorded says reviewer.json names a reviewer now. ReviewedBy is the
	// pane role that wrote the review; older records have none and count as builder
	// reviews.
	ReviewerRecorded                        bool
	ReviewerKindNow, ReviewerKindReviewed   string
	ReviewerModelNow, ReviewerModelReviewed string
	// ReviewerModelMeasuredNow and ReviewerModelMeasuredReviewed say whether
	// each side's model was measured. The model is compared only when both
	// were (ReviewerModelsMeasured); otherwise only the kind is.
	ReviewerModelMeasuredNow, ReviewerModelMeasuredReviewed bool
	ReviewedBy                                              string

	// There is deliberately no binding on the working tree. porcelain reports
	// status, not content: committing the plan between reviewing it and handing
	// it over removes its `??` line and would void the review for doing the
	// right thing. See the spec's "Two bindings".

	// Verdict is the builder's own assertion, "clean" or "objections". It is
	// asserted rather than parsed out of the review prose: a parser that
	// failed to find objections in a malformed review would report clean.
	Verdict string

	// OverrideReason is the planner's --because text. Non-empty means the
	// objections were overruled.
	OverrideReason string

	// ExpectedID is the review the caller believes it acts on, empty when it has no
	// expectation. NewestID is the one selected. They differ when a newer review
	// landed in between, and an override written to the older record would be
	// ignored.
	ExpectedID, NewestID string
}

// ReviewerModelsMeasured says the reviewer's model was measured both when the
// review was made and now, so the two can be compared.
func (in ReviewInput) ReviewerModelsMeasured() bool {
	return in.ReviewerModelMeasuredNow && in.ReviewerModelMeasuredReviewed
}

// side names one side of a binding for a person, "?" for what is unknown:
// kind/model for the reviewer, kind/autonomy for the builder.
func side(kind, second string) string {
	if kind == "" {
		kind = "?"
	}
	if second == "" {
		second = "?"
	}
	return kind + "/" + second
}

// reviewerModel is a reviewer side's model as a person reads it.
func reviewerModel(model string, measured bool) string {
	if !measured {
		return "unmeasured"
	}
	return model
}

// Review decides whether a plan may be handed to the builder.
//
// Code 24 (exitcode.ReviewObjections) means refused, and Detail[0] names the
// binding that failed so bash prints the matching remedy. A "reviewer" or
// "builder" refusal adds the reviewed side and the current side as Detail[1]
// and Detail[2]. The plan text is checked before builder identity, since the
// planner is more likely to have changed it on purpose.
func Review(in ReviewInput) Verdict {
	refuse := func(reason string) Verdict {
		return Verdict{Code: 24, Detail: []string{reason}}
	}

	// First of all: with no reviewer there is nobody to ask for a review, so
	// "no review exists" would send the planner to a command that cannot work.
	if !in.ReviewerRecorded {
		return refuse("no_reviewer")
	}
	if !in.Found {
		return refuse("absent")
	}
	if in.ExpectedID != "" && in.ExpectedID != in.NewestID {
		return refuse("superseded")
	}
	if !in.PlanMeasured || in.PlanNow != in.PlanReviewed {
		return refuse("plan")
	}
	if in.ReviewedBy != "reviewer" {
		return refuse("builder_review")
	}
	if in.ReviewerKindNow != in.ReviewerKindReviewed ||
		(in.ReviewerModelsMeasured() && in.ReviewerModelNow != in.ReviewerModelReviewed) {
		return Verdict{Code: 24, Detail: []string{"reviewer",
			side(in.ReviewerKindReviewed, reviewerModel(in.ReviewerModelReviewed, in.ReviewerModelMeasuredReviewed)),
			side(in.ReviewerKindNow, reviewerModel(in.ReviewerModelNow, in.ReviewerModelMeasuredNow))}}
	}
	if !in.IdentityMeasured ||
		in.KindNow != in.KindReviewed ||
		in.AutonomyNow != in.AutonomyReviewed {
		return Verdict{Code: 24, Detail: []string{"builder",
			side(in.KindReviewed, in.AutonomyReviewed), side(in.KindNow, in.AutonomyNow)}}
	}
	// Only an explicit "clean" is approval. A missing, misspelled or truncated
	// verdict is treated as objections, because the alternative is a malformed
	// sidecar silently opening the gate.
	if in.Verdict != "clean" {
		if strings.TrimSpace(in.OverrideReason) != "" {
			return Verdict{}
		}
		return refuse("objections")
	}
	return Verdict{}
}
