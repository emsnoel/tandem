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

func TestWriteReviewerShape(t *testing.T) {
	relay := t.TempDir()
	if err := WriteReviewer(relay, ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus", Autonomy: "auto", ModelMeasured: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(relay, ReviewerFile))
	want := `{"kind":"claude","name":"reviewer","model":"opus","autonomy":"auto","model_measured":true}` + "\n"
	if err != nil || string(b) != want {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestNextReviewerStartedWithModel(t *testing.T) {
	got := NextReviewer(nil, "claude", "reviewer", "opus", "auto", true)
	if got != (ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus", Autonomy: "auto", ModelMeasured: true}) {
		t.Fatalf("got %+v", got)
	}
}

func TestNextReviewerStartedNoModel(t *testing.T) {
	prev := &ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus", ModelMeasured: true}
	if got := NextReviewer(prev, "claude", "reviewer", "", "auto", true); got.Model != "" || got.ModelMeasured {
		t.Fatalf("got %+v", got)
	}
}

func TestNextReviewerKeepsMeasuredModel(t *testing.T) {
	prev := &ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus", ModelMeasured: true}
	got := NextReviewer(prev, "claude", "reviewer", "sonnet", "ask", false)
	if got.Model != "opus" || !got.ModelMeasured || got.Autonomy != "ask" {
		t.Fatalf("got %+v", got)
	}
}

func TestNextReviewerDropsModelOnKindOrNameChange(t *testing.T) {
	prev := &ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus", ModelMeasured: true}
	for _, c := range [][2]string{{"codex", "reviewer"}, {"claude", "rev2"}} {
		if got := NextReviewer(prev, c[0], c[1], "", "auto", false); got.Model != "" || got.ModelMeasured {
			t.Errorf("%v: got %+v", c, got)
		}
	}
	unmeasured := &ReviewerRecord{Kind: "claude", Name: "reviewer", Model: "opus"}
	if got := NextReviewer(unmeasured, "claude", "reviewer", "", "auto", false); got.Model != "" {
		t.Errorf("kept an unmeasured model: %+v", got)
	}
}
