package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/gate"
	"github.com/emsnoel/tandem/internal/paths"
)

func writeRelayFile(t *testing.T, root, name, body string) {
	t.Helper()
	dir := paths.Relay(root)
	if err := os.MkdirAll(filepath.Join(dir, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reviewInputFor reads the reviewer beside the builder, and the author and
// reviewer identity out of the stored review.
func TestReviewInputForReadsTheReviewer(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "p.md")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRelayFile(t, root, "builder.json", `{"kind":"codex","autonomy":"auto"}`)
	writeRelayFile(t, root, "reviewer.json", `{"kind":"claude","name":"reviewer","model":"opus","autonomy":"auto","model_measured":true}`)
	writeRelayFile(t, root, "reviews/20260101-000000-1-1.json",
		`{"plan":"`+plan+`","verdict":"clean","kind":"codex","autonomy":"auto","reviewed_by":"reviewer","reviewer_kind":"claude","reviewer_model":"opus","reviewer_model_measured":true}`)
	writeRelayFile(t, root, "reviews/20260101-000000-1-1.plan.md", "# plan\n")

	in, _ := reviewInputFor(plan, root)
	if !in.ReviewerRecorded || in.ReviewerKindNow != "claude" || in.ReviewerModelNow != "opus" ||
		in.ReviewerKindReviewed != "claude" || in.ReviewerModelReviewed != "opus" ||
		!in.ReviewerModelsMeasured() || in.ReviewedBy != "reviewer" {
		t.Fatalf("input = %+v", in)
	}
	if v := gate.Review(in); v.Code != 0 {
		t.Fatalf("gate = %+v, want pass", v)
	}
}

func TestReviewInputForWithoutReviewerRecord(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "p.md")
	_ = os.WriteFile(plan, []byte("# plan\n"), 0o644)
	writeRelayFile(t, root, "builder.json", `{"kind":"codex","autonomy":"auto"}`)
	writeRelayFile(t, root, "reviews/20260101-000000-1-1.json",
		`{"plan":"`+plan+`","verdict":"clean","kind":"codex","autonomy":"auto"}`)
	writeRelayFile(t, root, "reviews/20260101-000000-1-1.plan.md", "# plan\n")

	in, _ := reviewInputFor(plan, root)
	if in.ReviewerRecorded {
		t.Fatalf("ReviewerRecorded = true with no reviewer.json")
	}
	if v := gate.Review(in); v.Code != 24 || v.Detail[0] != "no_reviewer" {
		t.Fatalf("gate = %+v, want no_reviewer", v)
	}
}

// A model measured on only one side is not compared.
func TestReviewInputForModelMeasuredNeedsBothSides(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "p.md")
	_ = os.WriteFile(plan, []byte("# plan\n"), 0o644)
	writeRelayFile(t, root, "builder.json", `{"kind":"codex","autonomy":"auto"}`)
	writeRelayFile(t, root, "reviewer.json", `{"kind":"claude","name":"reviewer","model":"opus","model_measured":true}`)
	writeRelayFile(t, root, "reviews/20260101-000000-1-1.json",
		`{"plan":"`+plan+`","verdict":"clean","reviewed_by":"reviewer","reviewer_kind":"claude","reviewer_model_measured":false}`)
	in, _ := reviewInputFor(plan, root)
	if !in.ReviewerRecorded || in.ReviewerModelNow != "opus" {
		t.Fatalf("the reviewer record was not read: %+v", in)
	}
	if in.ReviewerModelsMeasured() {
		t.Fatalf("ReviewerModelsMeasured() = true with an unmeasured review")
	}
}

// tandem gate review prints a reviewer or builder refusal as the reason, the
// side the review was made with, the side now, and the review id, read from
// the real records. An unreadable record is "?" or "unmeasured", never an
// abort. bin/tandem-handoff prints these lines as they come.
func TestGateReviewPrintsWhatChanged(t *testing.T) {
	const id = "20260101-000000-1-1"
	setup := func(t *testing.T, builder, reviewer string) (root, plan string) {
		root = t.TempDir()
		plan = filepath.Join(root, "p.md")
		if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeRelayFile(t, root, "builder.json", builder)
		writeRelayFile(t, root, "reviewer.json", reviewer)
		writeRelayFile(t, root, "reviews/"+id+".json",
			`{"plan":"`+plan+`","verdict":"clean","kind":"codex","autonomy":"auto","reviewed_by":"reviewer","reviewer_kind":"claude","reviewer_model":"opus","reviewer_model_measured":true}`)
		writeRelayFile(t, root, "reviews/"+id+".plan.md", "# plan\n")
		return root, plan
	}
	for _, c := range []struct {
		name, builder, reviewer, want string
	}{
		{"a new reviewer model", `{"kind":"codex","autonomy":"auto"}`, `{"kind":"claude","name":"reviewer","model":"sonnet","model_measured":true}`,
			"reviewer\nclaude/opus\nclaude/sonnet\n" + id + "\n"},
		{"a new reviewer kind, unmeasured", `{"kind":"codex","autonomy":"auto"}`, `{"kind":"codex","name":"reviewer","model_measured":false}`,
			"reviewer\nclaude/opus\ncodex/unmeasured\n" + id + "\n"},
		{"a malformed builder.json", `not json at all`, `{"kind":"claude","name":"reviewer","model":"opus","model_measured":true}`,
			"builder\ncodex/auto\n?/?\n" + id + "\n"},
	} {
		root, plan := setup(t, c.builder, c.reviewer)
		if rc, out, _ := runCapture(t, "gate", "review", "--plan", plan, "--root", root); rc != 24 || out != c.want {
			t.Errorf("%s: rc %d out %q, want 24 %q", c.name, rc, out, c.want)
		}
	}
}
