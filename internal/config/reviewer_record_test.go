package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReviewerReadsACompleteRecord(t *testing.T) {
	relay := t.TempDir()
	writeReviewerRecord(t, relay, `{"kind":"codex","name":"critic","model":"gpt-x","autonomy":"auto","effort":"high","model_measured":true}`)
	got, err := LoadReviewer(relay)
	want := ReviewerRecord{Kind: "codex", Name: "critic", Model: "gpt-x", Autonomy: "auto", Effort: "high", ModelMeasured: true}
	if err != nil || got != want {
		t.Fatalf("LoadReviewer = %+v, %v; want %+v", got, err, want)
	}
}

func TestLoadReviewerTellsMissingFromIncomplete(t *testing.T) {
	relay := t.TempDir()
	if _, err := LoadReviewer(relay); !errors.Is(err, ErrNoReviewer) || errors.Is(err, ErrReviewerIncomplete) {
		t.Fatalf("no file: %v, want ErrNoReviewer", err)
	}
	for name, body := range map[string]string{
		"not json":  `not json`,
		"no name":   `{"kind":"claude"}`,
		"no kind":   `{"name":"reviewer"}`,
		"empty obj": `{}`,
	} {
		writeReviewerRecord(t, relay, body)
		if _, err := LoadReviewer(relay); !errors.Is(err, ErrReviewerIncomplete) || errors.Is(err, ErrNoReviewer) {
			t.Errorf("%s: %v, want ErrReviewerIncomplete", name, err)
		}
	}
}

func writeReviewerRecord(t *testing.T, relay, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(relay, ReviewerFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
