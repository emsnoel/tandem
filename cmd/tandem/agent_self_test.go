package main

import (
	"errors"
	"testing"

	"github.com/emsnoel/tandem/internal/herdr"
)

func TestSelfNameFindsTheCallersPane(t *testing.T) {
	b, p := "builder", "planner"
	agents := []herdr.Agent{{PaneID: "w1:p1", Name: &b}, {PaneID: "w1:p2", Name: &p}}
	if got := selfName(agents, "w1:p2"); got != "planner" {
		t.Fatalf("got %q", got)
	}
	if got := selfName(agents, "w1:p9"); got != "" {
		t.Fatalf("got %q for an unknown pane", got)
	}
}

// Outside Herdr HERDR_PANE_ID is unset. An agent Herdr lists with no pane id
// must not then be taken for the caller.
func TestSelfNameIsEmptyWithoutAPane(t *testing.T) {
	n := "stray"
	if got := selfName([]herdr.Agent{{PaneID: "", Name: &n}}, ""); got != "" {
		t.Fatalf("got %q with no pane", got)
	}
}

// decideReviewer is the fail-closed answer bin/tandem-verdict and
// bin/tandem-handback --review act on.
func TestDecideReviewer(t *testing.T) {
	b, r := "builder", "reviewer"
	panes := []herdr.Agent{{PaneID: "w1:p1", Name: &b}, {PaneID: "w1:p2", Name: &r}, {PaneID: "w1:p3"}}
	listed := func() ([]herdr.Agent, error) { return panes, nil }
	failing := func(err error) func() ([]herdr.Agent, error) {
		return func() ([]herdr.Agent, error) { return nil, err }
	}
	unreachable := &herdr.Error{Code: "unreachable", Message: `Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }`, Unreachable: true}
	timeout := &herdr.Error{Code: "timeout", Message: "herdr did not answer within 5s"}
	declined := &herdr.Error{Code: "internal", Message: "boom"}
	notCalled := func() ([]herdr.Agent, error) { t.Fatal("Herdr was asked with no pane id"); return nil, nil }
	for _, c := range []struct {
		name, pane, claimed string
		lookup              func() ([]herdr.Agent, error)
		want                reviewerVerdict
	}{
		{"builder pane claiming the reviewer role", "w1:p1", "reviewer", listed, reviewerVerdict{false, "other", "builder"}},
		{"reviewer pane", "w1:p2", "", listed, reviewerVerdict{true, "pane", "reviewer"}},
		{"reviewer pane claiming another role", "w1:p2", "builder", listed, reviewerVerdict{true, "pane", "reviewer"}},
		{"pane with no agent name", "w1:p3", "reviewer", listed, reviewerVerdict{false, "unnamed", ""}},
		{"pane Herdr does not list", "w1:p9", "reviewer", listed, reviewerVerdict{false, "unnamed", ""}},
		{"lookup times out", "w1:p1", "reviewer", failing(timeout), reviewerVerdict{false, "timeout", ""}},
		{"Herdr answers with an error", "w1:p1", "reviewer", failing(declined), reviewerVerdict{false, "error", declined.Error()}},
		{"unknown failure", "w1:p1", "reviewer", failing(errors.New("bad json")), reviewerVerdict{false, "error", "bad json"}},
		{"socket unreachable, reviewer role", "w1:p1", "reviewer", failing(unreachable), reviewerVerdict{true, "unreachable", "reviewer"}},
		{"socket unreachable, builder role", "w1:p1", "builder", failing(unreachable), reviewerVerdict{false, "role", "builder"}},
		{"no pane id, reviewer role", "", "reviewer", notCalled, reviewerVerdict{true, "role", "reviewer"}},
		{"no pane id, builder role", "", "builder", notCalled, reviewerVerdict{false, "role", "builder"}},
		{"no pane id, no role", "", "", notCalled, reviewerVerdict{false, "role", ""}},
	} {
		if got := decideReviewer(c.pane, c.claimed, "reviewer", c.lookup); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A custom reviewer name is what the pane and the role must match.
func TestDecideReviewerUsesTheRecordedName(t *testing.T) {
	r := "critic"
	listed := func() ([]herdr.Agent, error) { return []herdr.Agent{{PaneID: "w1:p2", Name: &r}}, nil }
	if got := decideReviewer("w1:p2", "", "critic", listed); !got.Accept {
		t.Fatalf("the pane named critic was refused: %+v", got)
	}
	if got := decideReviewer("", "reviewer", "critic", listed); got.Accept {
		t.Fatalf("role reviewer passed for a reviewer named critic: %+v", got)
	}
}
