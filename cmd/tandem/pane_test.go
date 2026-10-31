package main

import (
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestPaneSplitPrintsOnlyThePaneID(t *testing.T) {
	stubHerdrAnswers(t, map[string]string{"pane split": `{"result":{"pane":{"pane_id":"w1:p7"}}}`})
	out, rc := captureStdoutRC(t, func() int { return cmdPane([]string{"split", "w1:p1", "--direction", "right"}) })
	if rc != exitcode.OK || out != "w1:p7\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestPaneSplitWithoutIDFails(t *testing.T) {
	stubHerdrAnswers(t, map[string]string{"pane split": `{"result":{}}`})
	if out, rc := captureStdoutRC(t, func() int { return cmdPane([]string{"split", "w1:p1"}) }); rc != exitcode.Internal || out != "" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

func TestPaneSplitDirection(t *testing.T) {
	stubHerdrAnswers(t, map[string]string{"pane layout": `{"result":{"layout":{"panes":[{"pane_id":"w1:p1","rect":{"width":80,"height":60}}]}}}`})
	out, rc := captureStdoutRC(t, func() int { return cmdPane([]string{"split-direction", "w1:p1"}) })
	if got := fields(out); rc != exitcode.OK || len(got) != 3 || got[0] != "down" || got[1] != "80" || got[2] != "60" {
		t.Fatalf("rc %d, fields %q", rc, got)
	}
	if _, rc := captureStdoutRC(t, func() int { return cmdPane([]string{"split-direction", "w1:p9"}) }); rc != exitcode.HerdrError {
		t.Fatalf("unknown size: rc %d", rc)
	}
}

func TestTabLabel(t *testing.T) {
	stubHerdrAnswers(t, map[string]string{"tab get": `{"result":{"tab":{"label":"4","number":4}}}`})
	if out, rc := captureStdoutRC(t, func() int { return cmdTab([]string{"label", "w1:t1"}) }); rc != exitcode.OK || out != "default\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
	stubHerdrAnswers(t, map[string]string{"tab get": `{"result":{"tab":{"label":"mine","number":4}}}`})
	if out, rc := captureStdoutRC(t, func() int { return cmdTab([]string{"label", "w1:t1"}) }); rc != exitcode.OK || out != "named\n" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}
