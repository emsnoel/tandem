package decision

import (
	"strings"
	"testing"
)

// complete returns a well-formed batch with one answer overridden, so a
// rejection test fails for the reason it names. ParseStaffing reports the first
// missing answer, so tests start complete and break one thing, and assert on
// the error text.
func complete(override string, a Answer) map[string]Answer {
	m := map[string]Answer{
		QShape:       {Type: "choice", Choice: "mechanical", Confidence: 0.9},
		QRoutine:     {Type: "score", Score: 2.0, Confidence: 0.9},
		QSecurity:    {Type: "noul", Noul: 0.1},
		QDestructive: {Type: "noul", Noul: 0.1},
		QExternal:    {Type: "noul", Noul: 0.1},
		QParallel:    {Type: "noul", Noul: 0.1},
	}
	if override != "" {
		m[override] = a
	}
	return m
}

func TestParseStaffingAcceptsACompleteBatch(t *testing.T) {
	got, err := ParseStaffing(complete("", Answer{}))
	if err != nil {
		t.Fatalf("the complete batch must itself be valid, or every rejection test below is meaningless: %v", err)
	}
	if got.Shape != ShapeMechanical {
		t.Errorf("shape: want mechanical, got %q", got.Shape)
	}
	if got.RoutineProportion != 2.0 {
		t.Errorf("routine proportion: want the weighted float 2.0, got %v", got.RoutineProportion)
	}
}

func TestParseStaffingRejectsAMissingAnswer(t *testing.T) {
	m := complete("", Answer{})
	delete(m, QRoutine)
	_, err := ParseStaffing(m)
	if err == nil {
		t.Fatal("want an error when the batch came back incomplete, got nil")
	}
	if !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), QRoutine) {
		t.Fatalf("want a missing-answer error naming the absent key, got %v", err)
	}
}

func TestParseStaffingRejectsAnUnknownShape(t *testing.T) {
	_, err := ParseStaffing(complete(QShape, Answer{Type: "choice", Choice: "speculative", Confidence: 0.9}))
	if err == nil {
		t.Fatal("want an error for a shape outside the vocabulary, got nil")
	}
	if !strings.Contains(err.Error(), "speculative") {
		t.Fatalf("want the error to name the offending shape, got %v", err)
	}
}

func TestParseStaffingRejectsAWrongAnswerType(t *testing.T) {
	_, err := ParseStaffing(complete(QSecurity, Answer{Type: "score", Score: 0.1}))
	if err == nil {
		t.Fatal("want an error for an answer with the wrong provider type, got nil")
	}
	if !strings.Contains(err.Error(), QSecurity) || !strings.Contains(err.Error(), "score") {
		t.Fatalf("want an error naming the answer and its wrong type, got %v", err)
	}
}

// A JSON null decodes into a zero-valued Answer that looks present but has no
// type. It must be refused, not read as "noul 0".
func TestParseStaffingRejectsAZeroValuedAnswer(t *testing.T) {
	_, err := ParseStaffing(complete(QSecurity, Answer{}))
	if err == nil {
		t.Fatal("want an error for a present but empty answer, got nil")
	}
	if !strings.Contains(err.Error(), QSecurity) || !strings.Contains(err.Error(), "noul") {
		t.Fatalf("want an error naming the malformed answer and its expected type, got %v", err)
	}
}
