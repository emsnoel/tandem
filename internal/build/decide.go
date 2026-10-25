package build

// Outcome is what follows the end of a task: the task's new status, and
// whether the loop sends the next task, finishes, or stops (with a reason).
type Outcome struct {
	Status string
	Next   string // "next", "review", "fix", "finish" or "stop"
	// Reason for "stop": "stuck", "red", "budget", "review_edited_tree" or
	// "review_rounds"; "budget" on "finish" is a diagnostic.
	Reason string
}

func (o Outcome) String() string {
	if o.Next == "stop" {
		return "stop:" + o.Reason
	}
	return o.Next
}

// AdvanceFacts is everything DecideAdvance weighs when a task has just ended.
// CheckExit is nil when the project has no check command, Usage nil when the
// builder's log could not be read, and Budget 0 turns the budget off.
// Every task is reviewed by the reviewer pane, so ReviewComplete is true only
// once a review round has finished or been legitimately skipped.
// ReviewVerdict is the reviewer pane's verdict when the event being decided is
// its review handback ("clean" or "changes"), and "" otherwise. ReviewRounds
// counts the task's rounds with a verdict, this one included, and
// ReviewEditedTree says the reviewer changed the task's files or added one.
type AdvanceFacts struct {
	Stuck            bool
	CheckExit        *int
	Usage            *Usage
	Budget           int64
	IsLast           bool
	ReviewComplete   bool
	ReviewVerdict    string
	ReviewRounds     int
	ReviewEditedTree bool
}

// Exhausted reports whether the task's input has used up its budget. Reaching
// the limit counts. Unmeasured usage is never exhausted.
func (f AdvanceFacts) Exhausted() bool {
	return f.Budget > 0 && f.Usage != nil && f.Usage.Input >= f.Budget
}

// DecideAdvance judges a task that has just ended, in this order:
//   - the builder said it could not finish;
//   - the reviewer edited the tree, so its round is invalid;
//   - the check failed;
//   - the reviewer asked for changes: they go back to the builder, or the
//     build stops once MaxReviewRounds or the budget is spent;
//   - the review is not complete: request it, or stop with it pending when the
//     budget is spent, so spending never accepts an unreviewed task;
//   - the budget is spent: the task is done, but a person looks before the
//     next one;
//   - otherwise the next task is sent or the build finishes.
func DecideAdvance(f AdvanceFacts) Outcome {
	switch {
	case f.Stuck:
		return Outcome{Status: StatusStuck, Next: "stop", Reason: "stuck"}
	case f.ReviewEditedTree:
		return Outcome{Status: StatusStuck, Next: "stop", Reason: "review_edited_tree"}
	case f.CheckExit != nil && *f.CheckExit != 0:
		return Outcome{Status: StatusRed, Next: "stop", Reason: "red"}
	case f.ReviewVerdict == "changes" && f.ReviewRounds >= MaxReviewRounds:
		return Outcome{Status: StatusStuck, Next: "stop", Reason: "review_rounds"}
	case f.ReviewVerdict == "changes" && f.Exhausted():
		// A fix spends more on a task over its budget. The task is not
		// accepted; the planner decides whether to raise the budget.
		return Outcome{Status: StatusStuck, Next: "stop", Reason: "budget"}
	case f.ReviewVerdict == "changes":
		return Outcome{Status: StatusSent, Next: "fix"}
	case !f.ReviewComplete && f.ReviewVerdict != "clean":
		if f.Exhausted() {
			return Outcome{Status: StatusReviewing, Next: "stop", Reason: "budget"}
		}
		return Outcome{Status: StatusReviewing, Next: "review"}
	case f.IsLast && f.Exhausted():
		return Outcome{Status: StatusDone, Next: "finish", Reason: "budget"}
	case f.IsLast:
		return Outcome{Status: StatusDone, Next: "finish"}
	case f.Exhausted():
		return Outcome{Status: StatusDone, Next: "stop", Reason: "budget"}
	}
	return Outcome{Status: StatusDone, Next: "next"}
}

// Decide judges a task whose implementation and check outcome are already
// validated and that needs no review. It is DecideAdvance with the review
// already complete.
func Decide(stuck bool, checkExit *int, usage *Usage, budget int64, isLast bool) Outcome {
	return DecideAdvance(AdvanceFacts{Stuck: stuck, CheckExit: checkExit, Usage: usage, Budget: budget, IsLast: isLast, ReviewComplete: true})
}
