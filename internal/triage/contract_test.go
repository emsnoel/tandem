package triage

import (
	"math"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/decision"
)

func complete(override string, a decision.Answer) map[string]decision.Answer {
	m := map[string]decision.Answer{
		QImplements:  {Type: "noul", Noul: 0.95},
		QScope:       {Type: "noul", Noul: 0.02},
		QDestructive: {Type: "noul", Noul: 0.01},
		QPriority:    {Type: "score", Score: 0.1, Confidence: 0.8},
		QEvidence:    {Type: "noul", Noul: 0.9},
	}
	if override != "" {
		m[override] = a
	}
	return m
}

var okFacts = SkipFacts{CheckPassed: true, DiffComplete: true}

func cleanAnswers() Answers {
	return Answers{Implements: 0.95, ScopeExceeded: 0.02, Destructive: 0.01, Priority: 0.1, EvidenceSufficient: 0.95}
}

// The skip rule is a conjunction. A wrongly started review costs tokens while a
// wrongly skipped one ships a defect, so this enumerates each way it refuses.
func TestShouldSkipNeedsEveryClause(t *testing.T) {
	if !cleanAnswers().ShouldSkip(okFacts) {
		t.Fatal("a clean answer with complete passing evidence must be eligible")
	}
	if cleanAnswers().ShouldSkip(SkipFacts{DiffComplete: true}) {
		t.Fatal("a failing check must never skip, however clean the answer")
	}
	for name, mutate := range map[string]func(*Answers){
		"weak implements":   func(a *Answers) { a.Implements = 0.89 },
		"scope creep":       func(a *Answers) { a.ScopeExceeded = 0.11 },
		"destructive":       func(a *Answers) { a.Destructive = 0.11 },
		"weak evidence":     func(a *Answers) { a.EvidenceSufficient = 0.5 },
		"unresolved issues": func(a *Answers) { a.Priority = 3 },
	} {
		a := cleanAnswers()
		mutate(&a)
		if a.ShouldSkip(okFacts) {
			t.Errorf("%s must not skip", name)
		}
	}
}

func TestShouldSkipRejectsIncompleteEvidence(t *testing.T) {
	for name, f := range map[string]SkipFacts{
		"zero facts":      {},
		"diff incomplete": {CheckPassed: true},
		"truncated diff":  {CheckPassed: true, DiffComplete: true, Truncated: true},
		"check failed":    {DiffComplete: true},
	} {
		if cleanAnswers().ShouldSkip(f) {
			t.Errorf("%s must not skip", name)
		}
	}
}

func TestShouldSkipThresholdBoundaries(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*Answers)
		want   bool
	}{
		{"evidence exactly .90", func(a *Answers) { a.EvidenceSufficient = 0.90 }, true},
		{"evidence .899", func(a *Answers) { a.EvidenceSufficient = 0.899 }, false},
		{"priority exactly .25", func(a *Answers) { a.Priority = 0.25 }, true},
		{"priority .251", func(a *Answers) { a.Priority = 0.251 }, false},
		{"implements exactly .90", func(a *Answers) { a.Implements = 0.90 }, true},
		{"scope exactly .10", func(a *Answers) { a.ScopeExceeded = 0.10 }, true},
		{"destructive exactly .10", func(a *Answers) { a.Destructive = 0.10 }, true},
	} {
		a := cleanAnswers()
		c.mutate(&a)
		if got := a.ShouldSkip(okFacts); got != c.want {
			t.Errorf("%s: ShouldSkip = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestShouldSkipRejectsNonfiniteAnswers(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for name, set := range map[string]func(*Answers, float64){
			"implements":          func(a *Answers, v float64) { a.Implements = v },
			"scope_exceeded":      func(a *Answers, v float64) { a.ScopeExceeded = v },
			"destructive":         func(a *Answers, v float64) { a.Destructive = v },
			"priority":            func(a *Answers, v float64) { a.Priority = v },
			"evidence_sufficient": func(a *Answers, v float64) { a.EvidenceSufficient = v },
		} {
			a := cleanAnswers()
			set(&a, bad)
			if a.Validate() == nil {
				t.Errorf("%s = %v passed Validate", name, bad)
			}
			if a.ShouldSkip(okFacts) {
				t.Errorf("%s = %v skipped", name, bad)
			}
		}
	}
}

// The zero value must never skip. Every failure path in the design converges
// on "the reviewer runs", and a zero-valued Answers reaching ShouldSkip is
// exactly the shape a dropped error would take.
func TestZeroAnswersNeverSkip(t *testing.T) {
	if (Answers{}).ShouldSkip(okFacts) {
		t.Fatal("the zero value must not skip a review")
	}
}

func TestConfidentIgnoresEvidenceFacts(t *testing.T) {
	if !cleanAnswers().Confident() {
		t.Fatal("clean answers clear the model thresholds")
	}
	a := cleanAnswers()
	a.EvidenceSufficient = 0.5
	if a.Confident() {
		t.Fatal("weak evidence is a model answer and must fail Confident")
	}
}

func TestParseRejectsAMissingAnswer(t *testing.T) {
	m := complete("", decision.Answer{})
	delete(m, QScope)
	_, err := Parse(m)
	if err == nil {
		t.Fatal("want an error when the batch came back incomplete, got nil")
	}
	if !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), QScope) {
		t.Fatalf("want a missing-answer error naming the absent key, got %v", err)
	}
}

func TestParseRejectsAWrongAnswerType(t *testing.T) {
	_, err := Parse(complete(QImplements, decision.Answer{Type: "score", Score: 0.9}))
	if err == nil {
		t.Fatal("want an error for an answer with the wrong provider type, got nil")
	}
	if !strings.Contains(err.Error(), QImplements) || !strings.Contains(err.Error(), "score") {
		t.Fatalf("want an error naming the answer and its wrong type, got %v", err)
	}
}

func TestParseRejectsAProbabilityOutsideRange(t *testing.T) {
	_, err := Parse(complete(QImplements, decision.Answer{Type: "noul", Noul: 1.4}))
	if err == nil {
		t.Fatal("want an error for a noul outside [0,1], got nil")
	}
	if !strings.Contains(err.Error(), "implements") {
		t.Fatalf("want the error to name the offending field, got %v", err)
	}
}

func TestParseAcceptsACompleteBatch(t *testing.T) {
	got, err := Parse(complete("", decision.Answer{}))
	if err != nil {
		t.Fatalf("the complete batch must itself be valid, or every rejection test above is meaningless: %v", err)
	}
	if got.Implements != 0.95 {
		t.Errorf("implements: want 0.95, got %v", got.Implements)
	}
	if !got.ShouldSkip(okFacts) {
		t.Error("a clean parsed batch with complete passing evidence must be eligible")
	}
}
