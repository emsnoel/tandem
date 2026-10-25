package triage

import "github.com/emsnoel/tandem/internal/decision"

// Questions is the full batch, sent in one request: the brief and diff are sent
// once, so five questions cost about what one costs.
//
// No question asks the model to count, compare dates or do arithmetic, which
// are known Jev weaknesses; review priority is a score over described
// situations. Each question names the brief and the diff, because the question
// id is never sent.
func Questions() map[string]decision.Question {
	return map[string]decision.Question{
		QImplements: {
			Type: "noul",
			Instructions: "The task brief states one unit of work that was asked for. " +
				"The diff is the change that was made in response to it. " +
				"Does the diff implement what the brief asked for, in full?",
		},
		QScope: {
			Type: "noul",
			Instructions: "Does the diff change anything the task brief did not ask for? " +
				"Renaming, reformatting, refactoring nearby code, or altering behaviour " +
				"the brief never mentions all count. Changes the brief did ask for do not, " +
				"however large they are.",
		},
		QDestructive: {
			Type: "noul",
			Instructions: "Does the diff introduce an operation that destroys data or is " +
				"hard to undo -- deleting files or directories, dropping or truncating " +
				"stored data, force-pushing, or rewriting history?",
		},
		QPriority: {
			Type: "score",
			Instructions: "How much does this change need a human or a reviewing agent to " +
				"look at it before it is trusted? Judge the change in front of you, not the " +
				"general importance of the files it touches.",
			// Ordered list, not a map: the order defines the levels and a map
			// returns 422. Each level describes a concrete situation and stands
			// on its own, so the model is not asked to interpolate a scale.
			Criteria: []string{
				"The change is mechanical and self-evident: its effect can be seen by reading it, and nothing about it could surprise the author of the brief.",
				"The change is routine but has a detail worth confirming, such as a boundary condition or an error path.",
				"The change makes a judgement call the brief did not settle, or touches behaviour other code depends on.",
				"The change is risky or surprising: it alters authority, data handling, or an invariant, or it does something the brief did not anticipate.",
			},
		},
		QEvidence: {
			Type: "noul",
			Instructions: "Taken together, do the diff and the check-command output provide " +
				"enough evidence to tell whether the task actually works? Answer about the " +
				"evidence available, not about whether the change looks correct.",
		},
	}
}
