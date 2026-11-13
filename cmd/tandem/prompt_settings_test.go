package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
)

const reviewID = "20260910-204612-8821-19044"

// sentPrompt is the text of the first prompt the run sent.
func (e *cliEnv) sentPrompt() string {
	e.t.Helper()
	e.sub("tick")
	if e.tr.count() == 0 {
		e.t.Fatal("no prompt was sent")
	}
	id, _, err := state.LookupIdentity(e.root, e.home)
	if err != nil {
		e.t.Fatal(err)
	}
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		e.t.Fatal(err)
	}
	b, err := art.ReadAll(context.Background(), e.tr.sends[0].PromptHash, 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func TestStartPutsTheSettingsInTheTaskPrompt(t *testing.T) {
	e := newCLI(t)
	reviews := filepath.Join(paths.Relay(e.root), "reviews")
	os.MkdirAll(reviews, 0o755)
	var objection strings.Builder
	for i := 1; i <= 45; i++ {
		fmt.Fprintf(&objection, "objection line %d\n", i)
	}
	os.WriteFile(filepath.Join(reviews, reviewID+".md"), []byte(objection.String()), 0o644)
	rc, _, errOut := e.start("--conventions-file", "STYLE.md", "--prompt-extra", "Keep it short.",
		"--ruling-review", reviewID, "--ruling", "skip the commit steps")
	if rc != exitcode.OK {
		t.Fatalf("start: rc %d %q", rc, errOut)
	}
	prompt := e.sentPrompt()
	for _, want := range []string{"/STYLE.md in full before you start.", "Keep it short.", "  objection line 1\n",
		"  objection line 40\n", "  RULING: skip the commit steps"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "objection line 41") {
		t.Error("more than the first 40 lines of the objection were sent")
	}
}

func TestARulingNeedsItsReview(t *testing.T) {
	e := newCLI(t)
	for name, args := range map[string][]string{
		"ruling alone":    {"--ruling", "why"},
		"review alone":    {"--ruling-review", reviewID},
		"a malformed id":  {"--ruling-review", "../x", "--ruling", "why"},
		"an empty ruling": {"--ruling-review", reviewID, "--ruling", " "},
	} {
		if rc, _, errOut := e.start(args...); rc != exitcode.Usage {
			t.Errorf("%s: rc %d %q", name, rc, errOut)
		}
	}
}
