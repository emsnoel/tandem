package decision

// Answer keys. These are the map keys in both the request and the response, so
// they are the contract between Questions() and the parser.
const (
	QShape       = "work_shape"
	QRoutine     = "routine_proportion"
	QSecurity    = "touches_security"
	QDestructive = "touches_destructive"
	QExternal    = "touches_external_effects"
	QParallel    = "parallelizable"
)

// Question is provider-neutral so the policy can be tested against recorded
// answers or another model. Criteria is a map for a choice and an ordered list
// for a score, whose order defines the levels (a map gets a 422).
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Questions is the full batch, sent in one request. The state is sent once, so
// thirteen questions cost about what one costs, and questions nothing acts on
// yet still collect calibration labels.
//
// No question asks the model to count, compare dates or do arithmetic, which
// are known Jev weaknesses; proportion is a score over described levels.
func Questions() map[string]Question {
	return map[string]Question{
		QShape: {
			Type:         "choice",
			Instructions: "Classify the dominant character of the implementation work this plan describes.",
			Criteria: map[string]string{
				string(ShapeMechanical):    "The plan states exactly what to change and where. Success is verifiable by running the existing test suite. No investigation or design judgement is needed.",
				string(ShapeInvestigative): "The desired change is known but its cause or location is not. Someone must read around the codebase and form hypotheses before editing.",
				string(ShapeIntegration):   "Correctness depends on how several components interact. The risk lives in the seams between parts, not in any single file.",
			},
		},
		QRoutine: {
			Type:         "score",
			Instructions: "How much of this plan is routine execution that needs no design judgement?",
			Criteria: []string{
				"Essentially all of the work requires design judgement or investigation.",
				"A minority of the work is routine; most needs judgement.",
				"Most of the work is routine, with a small portion needing judgement.",
				"Every step is fully specified and mechanical.",
			},
		},
		QSecurity: {
			Type:         "noul",
			Instructions: "The work described in this plan touches authentication, authorisation, credentials, secrets, cryptography, or the handling of untrusted input.",
		},
		QDestructive: {
			Type:         "noul",
			Instructions: "The work described in this plan risks data loss, performs a schema migration, or deletes or overwrites existing data.",
		},
		QExternal: {
			Type:         "noul",
			Instructions: "The work described in this plan causes effects outside this repository, such as network calls to third-party services, sending messages, or publishing artefacts.",
		},
		QParallel: {
			Type:         "noul",
			Instructions: "The tasks in this plan are independent of one another and could be worked on at the same time without one blocking another.",
		},
	}
}
