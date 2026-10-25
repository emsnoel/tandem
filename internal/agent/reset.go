package agent

import "fmt"

// Settled reports whether a Herdr agent status means the agent finished its
// turn and waits for input. Herdr says "done" for a finished turn nobody has
// looked at and "idle" once someone has; both count.
func Settled(status string) bool {
	return status == "idle" || status == "done"
}

// ResetDecision says whether to reset a builder's conversation before a build
// or a review. "go" means reset; anything else is a skip token naming why not,
// checked in order so the most fundamental reason wins.
//
// empty is true only when the pane is known to have taken no turn. A pane whose
// conversation is unknown, such as a Codex pane with no reported session, is
// reset: resetting an empty conversation costs one command, while a task in a
// full one costs its tokens on every call. An unsupported kind is checked
// first.
//
// session is the conversation Herdr names for the pane, and lastBuild the
// session holding the running build's task, which is never reset. build
// advance, stop and finish clear lastBuild.
func ResetDecision(cmd, status string, empty bool, session, lastBuild string) string {
	switch {
	case cmd == "":
		return "skip:unsupported"
	case !Settled(status):
		return "skip:not-idle"
	case empty:
		return "skip:no-session"
	case session != "" && session == lastBuild:
		return "skip:holds-build"
	}
	return "go"
}

// ResetSentence is the one place a `tandem agent reset` token becomes a
// sentence, shared by bin/tandem-handoff and bin/tandem-review. purpose is
// what the fresh conversation is for: "build" or "review". who is the role of
// the pane that was reset: "builder", or "reviewer" for a plan review in a
// pair with a reviewer pane. Only the skips that cost tokens start with
// WARNING; holds-build is expected, and no-session means the pane has not
// taken a turn yet, so its conversation is empty.
func ResetSentence(tok, purpose, who string) string {
	switch tok {
	case "reset", "sent", "would-reset":
		return fmt.Sprintf("fresh conversation for the %s (%s).", purpose, tok)
	case "skip:holds-build":
		return fmt.Sprintf("the %s's conversation already holds a build; keeping it.", who)
	case "skip:unsupported":
		return fmt.Sprintf("WARNING no measured way to reset this %s kind; the %s shares its current conversation.", who, purpose)
	case "skip:not-idle":
		return fmt.Sprintf("WARNING the %s is still working or at a dialog, so its conversation was not reset.", who)
	case "skip:no-session":
		return fmt.Sprintf("the %s has no conversation yet (Herdr reports no session), so there is nothing to reset.", who)
	case "skip:no-identity":
		return fmt.Sprintf("WARNING no %s identity is recorded (run tandem-pair), so its conversation was not reset.", who)
	case "skip:off-main":
		return fmt.Sprintf("WARNING the %s's pane shows a subagent or a picker, not its conversation, so nothing was typed to reset it.", who)
	}
	why := tok
	if why == "" {
		why = "no answer"
	}
	return fmt.Sprintf("WARNING could not reset the %s's conversation (%s); sending the %s anyway.", who, why, purpose)
}
