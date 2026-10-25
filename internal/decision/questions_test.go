package decision

import "testing"

func TestEveryQuestionTheAnswersNeedIsAsked(t *testing.T) {
	qs := Questions()
	for _, k := range []string{QShape, QRoutine, QSecurity, QDestructive, QExternal, QParallel} {
		if _, ok := qs[k]; !ok {
			t.Errorf("no question defined for answer key %q", k)
		}
	}
}

// Choice criteria is a map keyed by option; Score criteria is an ordered list.
// A map for a Score gets a 422 from the API.
func TestChoiceCriteriaIsAMapAndScoreCriteriaIsAList(t *testing.T) {
	qs := Questions()
	if _, ok := qs[QShape].Criteria.(map[string]string); !ok {
		t.Errorf("choice criteria must be a map, got %T", qs[QShape].Criteria)
	}
	if _, ok := qs[QRoutine].Criteria.([]string); !ok {
		t.Errorf("score criteria must be an ordered list, got %T", qs[QRoutine].Criteria)
	}
}

func TestShapeOptionsMatchTheShapeVocabularyExactly(t *testing.T) {
	crit := Questions()[QShape].Criteria.(map[string]string)
	if len(crit) != len(Shapes) {
		t.Fatalf("want %d options, got %d", len(Shapes), len(crit))
	}
	for _, s := range Shapes {
		if _, ok := crit[string(s)]; !ok {
			t.Errorf("shape %q has no criterion, so the model can never return it", s)
		}
	}
}

func TestScoreHasFourLevels(t *testing.T) {
	if got := len(Questions()[QRoutine].Criteria.([]string)); got != 4 {
		t.Fatalf("want 4 ordered levels to match RoutineProportion's 0..3 range, got %d", got)
	}
}

// Nouls must not ask two things at once; a compound question makes the
// probability uninterpretable.
func TestNoulsAreSingleClaims(t *testing.T) {
	qs := Questions()
	for _, k := range []string{QSecurity, QDestructive, QExternal, QParallel} {
		if qs[k].Type != "noul" {
			t.Errorf("%s should be a noul, got %q", k, qs[k].Type)
		}
		if qs[k].Criteria != nil {
			t.Errorf("%s is a noul and must not carry criteria", k)
		}
	}
}
