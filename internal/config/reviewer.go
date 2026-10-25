package config

import "github.com/emsnoel/tandem/internal/agent"

// NoReviewerRemedy is the one remedy every Go refusal for a missing or unusable
// reviewer names. bin/lib/reviewer.sh holds the same sentence for the scripts.
const NoReviewerRemedy = `Declare a "reviewer" block in .tandem.json and re-run tandem-pair, or run tandem-pair --reviewer KIND`

// ReviewerStatus says whether a reviewer is configured and usable.
type ReviewerStatus string

const (
	// ReviewerNone: nothing asked for a reviewer. This is not an error; the
	// caller decides what a build without a reviewer does.
	ReviewerNone ReviewerStatus = "none"
	// ReviewerResolved: a reviewer is on and has a kind.
	ReviewerResolved ReviewerStatus = "resolved"
	// ReviewerInvalid: a reviewer was asked for but cannot be launched.
	ReviewerInvalid ReviewerStatus = "invalid"
)

// ReviewerResolution is the outcome of ResolveReviewer. Resolved is set only
// when Status is ReviewerResolved; Reason only otherwise.
type ReviewerResolution struct {
	Status   ReviewerStatus `json:"status"`
	Reason   string         `json:"reason,omitempty"`
	Resolved *Resolved      `json:"resolved,omitempty"`
}

// ResolveReviewer resolves the reviewer from its own sources only: the
// --kind/--model/--effort/--autonomy flags, HERDR_REVIEWER_*, and the repo's
// reviewer block. It never reads the builder block, HERDR_BUILDER_*,
// defaults.builder_kind or the user's per-adapter model, effort and autonomy,
// because those describe the builder's choices and a reviewer that inherited
// them would silently run the builder's model.
//
// A reviewer is on only when the repo has a reviewer block or a flag names a
// kind. HERDR_REVIEWER_KIND refines a reviewer that is on and never turns one
// on, so a stray variable cannot start an unreviewed pairing.
func ResolveReviewer(repo *Repo, user *User, getenv func(string) string, flags Overrides) ReviewerResolution {
	if repo.Reviewer == nil && flags.Kind == "" {
		return ReviewerResolution{Status: ReviewerNone,
			Reason: "no reviewer is configured. " + NoReviewerRemedy + ". tandem config resolve takes --kind as well"}
	}
	rr := repo.Reviewer
	if rr == nil {
		rr = &RepoRole{}
	}

	r := Resolved{Role: agent.RoleReviewer}
	r.Kind = pick(
		candidate{flags.Kind, FromFlag},
		candidate{getenv("HERDR_REVIEWER_KIND"), FromEnv},
		candidate{rr.Kind, FromRepo},
	)
	if r.Kind.Value == "" {
		return ReviewerResolution{Status: ReviewerInvalid,
			Reason: "the reviewer block sets no kind: set reviewer.kind in .tandem.json, or pass --kind"}
	}

	r.Model = pick(
		candidate{flags.Model, FromFlag},
		candidate{getenv("HERDR_REVIEWER_MODEL"), FromEnv},
		candidate{rr.Model, FromRepo},
	)
	r.Effort = pick(
		candidate{flags.Effort, FromFlag},
		candidate{getenv("HERDR_REVIEWER_EFFORT"), FromEnv},
		candidate{rr.Effort, FromRepo},
	)
	r.Autonomy = pick(
		candidate{flags.Autonomy, FromFlag},
		candidate{getenv("HERDR_REVIEWER_AUTONOMY"), FromEnv},
		candidate{rr.Autonomy, FromRepo},
		candidate{"auto", FromDefault},
	)

	// Only what describes the CLI on this machine carries over from the user's
	// adapter entry. Its model, effort and autonomy are the builder's picks.
	ua := user.Adapters[r.Kind.Value]
	r.AllowDegrade = union(rr.AllowDegrade, ua.AllowDegrade)
	r.AllowWiden = ua.AllowWiden
	r.ExtraArgs = ua.Args
	r.Path = ua.Path
	return ReviewerResolution{Status: ReviewerResolved, Resolved: &r}
}
