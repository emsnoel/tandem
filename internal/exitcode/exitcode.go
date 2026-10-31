// Package exitcode holds tandem's process exit codes.
//
// One vocabulary across the whole binary: 4-9 mean "a gate refused" and
// nothing else, which is why a herdr error is 10 rather than the obvious 4.
package exitcode

const (
	OK          = 0
	Internal    = 1
	Usage       = 2
	Unreachable = 3
	// 4-9 are reserved for gate verdicts; see internal/gate.
	HerdrError = 10
	// 11-14 are unused, so old transcripts stay unambiguous.
	//
	// 20-23 are adapter and configuration refusals, in their own block so they
	// can never be mistaken for HerdrError.
	NarrowingRefused = 20
	WideningRefused  = 21
	// KindUnapproved: a repository asked for an agent kind this user has not
	// approved for that repository.
	KindUnapproved = 22
	// ManualGate: a builder cannot launch because a pre-launch gate needs a
	// human. For a planner the same finding is only a note.
	ManualGate = 23

	// ReviewObjections: a plan was refused before it reached the builder: no
	// current review exists, or its objections stand. See internal/gate's Review.
	// Like 4-9, internal/gate spells the number to stay free of dependencies.
	ReviewObjections = 24

	// NeverStarted and TurnEndedAtOnce: `tandem agent prompt --as` typed a
	// prompt into a settled pane and no turn of work followed. Either it never
	// started, as at a dialog Herdr cannot see, or it ended at once, as on a
	// model the CLI rejects. The scripts report these as their own 7 and 8.
	NeverStarted    = 25
	TurnEndedAtOnce = 26

	// AgentExited: `tandem agent alive` watched an agent that was just
	// started, and its CLI quit: Herdr stopped listing it, or its pane is back
	// at the shell.
	AgentExited = 27

	// NoReviewer and ReviewerIncomplete: `tandem reviewer show` found no
	// reviewer.json, or one that cannot be read or names no kind and pane.
	// The scripts explain each in their own words.
	NoReviewer         = 28
	ReviewerIncomplete = 29

	// DoctorFailed: `tandem doctor` found at least one problem that would stop
	// a pairing. It prints each one with its fix; warnings alone exit 0.
	DoctorFailed = 30
)
