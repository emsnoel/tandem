package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
)

// A review prompt for a reviewer pane is typed into that pane, under the same
// settled check as any review, and the pane is never reset: it is not a
// builder starting a task.
func TestHerdrReviewPromptGoesToTheReviewerPaneUnreset(t *testing.T) {
	p := newPane("idle")
	rc, err := transportFor(p).Send(context.Background(), delivery("review_prompt", "reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	if p.resets != 0 || len(p.prompts) != 1 || p.prompts[0] != "reviewer: PROMPT h1" {
		t.Fatalf("resets %d prompts %q", p.resets, p.prompts)
	}
	if rc.ExternalID != "s1" {
		t.Fatalf("the receipt names %q, not the reviewer's conversation", rc.ExternalID)
	}

	busy := newPane("working")
	if _, err := transportFor(busy).Send(context.Background(), delivery("review_prompt", "reviewer")); !runner.IsNotSent(err) {
		t.Fatalf("a review typed into a working reviewer: %v", err)
	}
	if len(busy.prompts) != 0 || busy.resets != 0 {
		t.Fatalf("a working reviewer was typed into (%q) or reset (%d)", busy.prompts, busy.resets)
	}
}

func TestDefaultTransportChecksTheReviewerKind(t *testing.T) {
	spec := transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: t.TempDir(), ReviewerKind: "claude", Reviewer: "reviewer"}
	if _, err := defaultDurableTransport(spec); err != nil {
		t.Fatalf("a registered reviewer kind: %v", err)
	}
	spec.ReviewerKind = "nonesuch"
	if _, err := defaultDurableTransport(spec); err == nil || !strings.Contains(err.Error(), "nonesuch") {
		t.Fatalf("an unregistered reviewer kind: %v", err)
	}
	spec.ReviewerKind, spec.Reviewer = "", "reviewer"
	if _, err := defaultDurableTransport(spec); err == nil {
		t.Fatal("a reviewer pane with no kind")
	}
	spec.ReviewerKind, spec.Reviewer = "claude", ""
	if _, err := defaultDurableTransport(spec); err == nil {
		t.Fatal("a reviewer kind with no pane")
	}
	spec.ReviewerKind, spec.Reviewer = "", ""
	if _, err := defaultDurableTransport(spec); err == nil {
		t.Fatal("a durable transport with no reviewer pane")
	}
}

// With neither flag, start reads the pane tandem-pair recorded, the way the
// legacy start and an import do. A record that is missing or half-written
// is refused with the remedy, and nothing is sent.
func TestDurableStartReadsTheRecordedReviewer(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	relay := paths.Relay(e.root)
	startBare := func() (int, string, string) {
		return e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder", "builder", "--planner", "planner",
			"--builder-kind", "codex", "--check", "true", "--state-home", e.home)
	}
	for name, rec := range map[string]string{"no name": `{"kind":"claude"}`, "not json": `not json`, "the builder": `{"kind":"claude","name":"builder"}`} {
		if err := os.WriteFile(filepath.Join(relay, "reviewer.json"), []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
		if rc, _, errOut := startBare(); rc != exitcode.ManualGate || !strings.Contains(errOut, "tandem-pair --reviewer KIND") && !strings.Contains(errOut, "is the builder") {
			t.Fatalf("%s: rc %d %q", name, rc, errOut)
		}
		if e.tr.count() != 0 {
			t.Fatalf("%s: a refused start sent something", name)
		}
	}
	if err := os.WriteFile(filepath.Join(relay, "reviewer.json"), []byte(`{"kind":"claude","name":"critic"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out, errOut := startBare()
	if rc != exitcode.OK {
		t.Fatalf("start rc %d %s", rc, errOut)
	}
	id, _, _ := state.LookupIdentity(e.root, e.home)
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := controller.RunAgents(context.Background(), st, art, oneJSON(t, out)["run_id"].(string))
	if err != nil || ag.ReviewerKind != "claude" || ag.ReviewerSession != "critic" {
		t.Fatalf("agents %+v %v", ag, err)
	}
}

// build start records the reviewer pane in the run, and every later command
// builds its transport with it.
func TestDurableStartRecordsTheReviewerPane(t *testing.T) {
	e := newCLI(t)
	var specs []transportSpec
	durableTransport = func(s transportSpec) (runner.Transport, error) { specs = append(specs, s); return e.tr, nil }
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, got, errOut := e.start("--reviewer", "reviewer", "--reviewer-kind", "claude")
	if rc != exitcode.OK {
		t.Fatalf("start rc %d %s", rc, errOut)
	}
	id, _, _ := state.LookupIdentity(e.root, e.home)
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := controller.RunAgents(context.Background(), st, art, got["run_id"].(string))
	if err != nil || ag.ReviewerKind != "claude" || ag.ReviewerSession != "reviewer" {
		t.Fatalf("agents %+v %v", ag, err)
	}
	if rc, _, errOut := e.sub("tick"); rc != exitcode.OK {
		t.Fatalf("tick rc %d %s", rc, errOut)
	}
	last := specs[len(specs)-1]
	if last.Reviewer != "reviewer" || last.ReviewerKind != "claude" || last.Builder != "builder" {
		t.Fatalf("the tick's transport was built from %+v", last)
	}
}

func TestDurableStartRefusesAnIncompleteOrSelfReviewingPane(t *testing.T) {
	for name, extra := range map[string][]string{
		"a pane with no kind": {"--reviewer", "reviewer"},
		"a kind with no pane": {"--reviewer-kind", "claude"},
		"the builder's pane":  {"--reviewer", "builder", "--reviewer-kind", "claude"},
		"the planner's pane":  {"--reviewer", "planner", "--reviewer-kind", "claude"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCLI(t)
			t.Setenv("TANDEM_BUILD_ENGINE", "durable")
			if rc, _, errOut := e.start(extra...); rc == exitcode.OK || !strings.Contains(errOut, "review") {
				t.Fatalf("rc %d %q", rc, errOut)
			}
			if e.tr.count() != 0 {
				t.Fatal("a refused start sent something")
			}
		})
	}
}

// Neither flag and no record is a pane too few: a durable run needs a
// reviewer, and the builder never reviews.
func TestDurableStartRefusesWithoutAReviewerPane(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if err := os.Remove(filepath.Join(paths.Relay(e.root), "reviewer.json")); err != nil {
		t.Fatal(err)
	}
	rc, _, errOut := e.run("build", "start", "--plan", e.plan, "--root", e.root, "--builder", "builder", "--planner", "planner",
		"--builder-kind", "codex", "--check", "true", "--state-home", e.home)
	if rc == exitcode.OK || !strings.Contains(errOut, "a run needs a reviewer pane: the builder never reviews") || !strings.Contains(errOut, "tandem-pair --reviewer KIND") {
		t.Fatalf("rc %d %q", rc, errOut)
	}
	if e.tr.count() != 0 {
		t.Fatal("a refused start sent something")
	}
}

func TestNoReviewerPauseNamesTheWayOn(t *testing.T) {
	why := fixedPauseReason("no_reviewer")
	for _, want := range []string{"tandem state archive --run ID", "tandem-pair --reviewer KIND"} {
		if !strings.Contains(why, want) {
			t.Errorf("the pause reason %q does not say %q", why, want)
		}
	}
	// The full restart sequence, in order: an archive refuses while a stopped
	// run's builder job is open, and state recover ends it.
	last := -1
	for _, step := range []string{"tandem build stop", "tandem state recover --run ID", "tandem state archive --run ID", "tandem-pair --reviewer KIND", "hand off again"} {
		i := strings.Index(why, step)
		if i <= last {
			t.Errorf("the pause reason %q does not say %q after the step before it", why, step)
		}
		last = i
	}
}
