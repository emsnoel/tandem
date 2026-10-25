package triage

import "testing"

// Questions() and Parse() are two halves of one contract: the map keys are
// what the request is built from and what the response is read by. A question
// asked but never parsed is a token cost with no consumer; a key parsed but
// never asked is a guaranteed "missing from the batch" at runtime.
func TestEveryQuestionAskedIsAlsoParsed(t *testing.T) {
	asked := Questions()
	for _, key := range []string{QImplements, QScope, QDestructive, QPriority, QEvidence} {
		if _, ok := asked[key]; !ok {
			t.Errorf("%q is parsed but never asked", key)
		}
	}
	if len(asked) != 5 {
		t.Errorf("want 5 questions, got %d -- a new one needs a field in Answers and a case in Parse", len(asked))
	}
}

// A Score's criteria must be an ordered list: the order defines the levels, and
// a map gets a 422.
func TestScoreCriteriaIsAnOrderedList(t *testing.T) {
	q, ok := Questions()[QPriority]
	if !ok {
		t.Fatalf("%q is missing from the question set", QPriority)
	}
	if q.Type != "score" {
		t.Fatalf("%q must be a score, got %q", QPriority, q.Type)
	}
	levels, ok := q.Criteria.([]string)
	if !ok {
		t.Fatalf("a score's criteria must be []string, got %T -- a map returns 422", q.Criteria)
	}
	if len(levels) < 2 {
		t.Fatalf("a score needs at least two levels, got %d", len(levels))
	}
}

// Every question must carry instructions. The question ID is not sent to the
// model, so an empty instruction is a question the model cannot answer.
func TestEveryQuestionCarriesInstructions(t *testing.T) {
	for key, q := range Questions() {
		if q.Instructions == "" {
			t.Errorf("%q has no instructions; the key itself is never sent to the model", key)
		}
		if q.Type == "" {
			t.Errorf("%q has no type", key)
		}
	}
}
