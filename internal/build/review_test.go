package build

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestChangedTreeCountsTheTasksFilesAndNewFilesOnly(t *testing.T) {
	r := &Review{Tree: map[string]string{"a": "1", "b": "2"}}
	cases := []struct {
		name  string
		after map[string]string
		want  bool
		other int
	}{
		{"same", map[string]string{"a": "1", "b": "2"}, false, 0},
		{"task file edited", map[string]string{"a": "9", "b": "2"}, true, 0},
		{"new file", map[string]string{"a": "1", "b": "2", "c": "3"}, true, 0},
		{"task file removed", map[string]string{"b": "2"}, true, 0},
		{"someone else's dirty file edited", map[string]string{"a": "1", "b": "9"}, false, 1},
	}
	for _, c := range cases {
		got, other := r.ChangedTree(c.after, []string{"a"})
		if got != c.want || len(other) != c.other {
			t.Errorf("%s: got %v %v", c.name, got, other)
		}
	}
}

func TestAcceptsEventRefusesARepeatOfTheFirstHandback(t *testing.T) {
	r := &Review{SentAt: "2026-09-29T10:00:05Z", FirstEvent: "e1"}
	if r.AcceptsEvent("e1", "2026-09-29T10:03:00Z") {
		t.Error("the first event's own id must not close the round")
	}
	if r.AcceptsEvent("e2", "2026-09-29T10:00:04Z") {
		t.Error("an event created before the prompt went out must not close the round")
	}
	if !r.AcceptsEvent("e3", "2026-09-29T10:03:00Z") {
		t.Error("a later event with a new id must close the round")
	}
	if !r.AcceptsEvent("e4", "") {
		t.Error("a queue file with no created time (older bridge) must not block the round forever")
	}
}

func TestReviewPromptAddressesTheReviewer(t *testing.T) {
	p := ReviewPrompt(2, "Farewell", "/relay/build/task-2.md", "critic")
	for _, want := range []string{"task 2", "Farewell", "/relay/build/task-2.md", "git diff HEAD", "Do not edit",
		"tandem-handback --task 2 --review --role critic --verdict clean|changes --report /relay/build/task-2.findings.md"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt misses %q:\n%s", want, p)
		}
	}
	for _, banned := range []string{"subagent", "fork_turns"} {
		if strings.Contains(p, banned) {
			t.Errorf("prompt still says %q: the reviewer pane reviews, no subagent:\n%s", banned, p)
		}
	}
}

func TestFixPromptSendsTheReviewToTheBuilder(t *testing.T) {
	p := FixPrompt(2, "Farewell", "/relay/build/task-2.review.md")
	for _, want := range []string{"task 2", "Farewell", "/relay/build/task-2.review.md", "Do not commit", "tandem-handback --task 2 --report"} {
		if !strings.Contains(p, want) {
			t.Errorf("fix prompt misses %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "--review") {
		t.Errorf("the builder never hands back a review:\n%s", p)
	}
}

func TestReviewCompleteNeedsPositiveEvidence(t *testing.T) {
	yes, no := true, false
	for name, c := range map[string]struct {
		r    *Review
		want bool
	}{
		"nil":                    {nil, false},
		"round with no data":     {&Review{}, false},
		"reviewer not seen":      {&Review{Reviewed: &no}, false},
		"clean verdict only":     {&Review{Verdict: "clean"}, false},
		"reviewer said clean":    {&Review{Reviewed: &yes, Result: "clean"}, true},
		"reviewer asked changes": {&Review{Reviewed: &yes, Result: "changes"}, false},
		"result without verdict": {&Review{Reviewed: &yes}, false},
		"clean but edited":       {&Review{Reviewed: &yes, Result: "clean", Changed: &yes}, false},
		"clean and read only":    {&Review{Reviewed: &yes, Result: "clean", Changed: &no}, true},
		"skipped by gate":        {&Review{Skipped: true}, true},
	} {
		if got := c.r.Complete(); got != c.want {
			t.Errorf("%s: Complete = %v, want %v", name, got, c.want)
		}
	}
}

func TestReviewPromptIDIsStableAndNamesThePromptItCovers(t *testing.T) {
	id := ReviewPromptID()
	if len(id) != 64 || id != ReviewPromptID() {
		t.Fatalf("%q", id)
	}
	// It is computed from the prompt itself, so a reworded prompt cannot keep it.
	sum := sha256.Sum256([]byte(ReviewPrompt(1, "title", "brief.md", "reviewer")))
	if id != hex.EncodeToString(sum[:]) {
		t.Error("the identity is not the hash of the rendered prompt")
	}
	// The reviewer pane's name is an input, not a different prompt: the
	// cohort is the same whatever the pane is called.
	if ReviewPrompt(1, "title", "brief.md", "critic") == ReviewPrompt(1, "title", "brief.md", "reviewer") {
		t.Error("the prompt does not name the reviewer pane it is sent to")
	}
}
