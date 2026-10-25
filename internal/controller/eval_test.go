package controller

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/evalrow"
	"github.com/emsnoel/tandem/internal/workflow"
)

func TestEvalLifecycleUsesController(t *testing.T) {
	sc := scenario(t, "tiny-2")
	e := newEvalEnv(t, sc)
	if err := e.begin(); err != nil {
		t.Fatal(err)
	}
	red := 0
	for _, task := range e.tasks() {
		e.playTask(task.ID, task.Ordinal, &red)
	}
	// Every state change went through the store's event log.
	evs, err := e.st.RunEvents(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{workflow.EventBuilderStarted, workflow.EventBuilderSubmitted, workflow.EventCheckCompleted, workflow.EventReviewCompleted}
	for _, task := range e.tasks() {
		att := e.current(task.ID)
		var got []string
		for _, ev := range evs {
			if ev.AttemptID == att.ID {
				got = append(got, ev.Kind)
			}
		}
		if len(got) < len(want) {
			t.Fatalf("task %s: events %v, want %v in order", task.ID, got, want)
		}
		i := 0
		for _, k := range got {
			if i < len(want) && k == want[i] {
				i++
			}
		}
		if i != len(want) {
			t.Errorf("task %s: events %v do not contain %v in order", task.ID, got, want)
		}
		if task.AcceptedAttemptID != att.ID {
			t.Errorf("task %s was not accepted through the controller", task.ID)
		}
	}
	if _, err := os.Stat(filepath.Join(e.relay, "build", "build.json")); !os.IsNotExist(err) {
		t.Error("the durable arm kept a legacy build record")
	}
	if st, _ := e.runState(); st != "finished" {
		t.Errorf("run %s", st)
	}
}

func TestEvalFailureIncludedInReport(t *testing.T) {
	row := runEval(t, scenario(t, "send-crash"), "durable", "pair-001")
	if row.State != "paused" || row.PauseCode != "writer_unknown" {
		t.Fatalf("the failed run is not a row: %+v", row)
	}
	if row.FailedTasks != 1 || row.AcceptedTasks != 0 {
		t.Errorf("failure count %d accepted %d", row.FailedTasks, row.AcceptedTasks)
	}
	// A restart after an uncertain send must not send the prompt again.
	e := newEvalEnv(t, scenario(t, "send-crash"))
	if err := e.begin(); err != nil {
		t.Fatal(err)
	}
	e.crashAfterFirstPrompt()
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Errorf("%d builder prompts after the restart", got)
	}
}

func TestEvalPartialUsageNotZero(t *testing.T) {
	row := runEval(t, scenario(t, "partial-usage"), "durable", "pair-001")
	if row.UsageCompleteness != workflow.UsagePartial || row.Reason == "" {
		t.Fatalf("a partial run must say so: %+v", row)
	}
	if row.InputTokens == nil || *row.InputTokens == 0 {
		t.Errorf("partial usage is a lower bound, not zero: %v", row.InputTokens)
	}
	if row.AcceptedTasks != 2 {
		t.Errorf("the run still finished its tasks: %+v", row)
	}
}

// failingTransport stands in for every provider: any call is a failure.
type failingTransport struct{ calls *int }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*f.calls++
	return nil, http.ErrHandlerTimeout
}

func TestEvalNoLiveCallsByDefault(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	for _, bin := range []string{"codex", "claude", "copilot", "opencode", "herdr"} {
		script := "#!/bin/sh\necho " + bin + " >> " + marker + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TYPESAFE_API_KEY", "must-not-be-used")
	calls := 0
	old := http.DefaultTransport
	http.DefaultTransport = failingTransport{&calls}
	t.Cleanup(func() { http.DefaultTransport = old })

	for _, sc := range loadScenarios(t).Scenarios {
		row := runEval(t, sc, "durable", "pair-001")
		exp := sc.Expect
		if row.State != exp.State || row.PauseCode != exp.PauseCode || row.AcceptedTasks != exp.Accepted || row.FailedTasks != exp.Failed {
			t.Errorf("%s: got %s/%s accepted %d failed %d, want %s/%s accepted %d failed %d", sc.ID,
				row.State, row.PauseCode, row.AcceptedTasks, row.FailedTasks, exp.State, exp.PauseCode, exp.Accepted, exp.Failed)
		}
		if exp.Usage != "" && row.UsageCompleteness != exp.Usage {
			t.Errorf("%s: usage %s, want %s", sc.ID, row.UsageCompleteness, exp.Usage)
		}
		if row.Mode != "fixture" {
			t.Errorf("%s: mode %q", sc.ID, row.Mode)
		}
	}
	if calls != 0 {
		t.Errorf("%d network calls", calls)
	}
	if _, err := os.Stat(marker); err == nil {
		b, _ := os.ReadFile(marker)
		t.Errorf("an agent was started: %s", b)
	}
}

// TestEvalRows writes one durable-arm row per scenario to TANDEM_EVAL_OUT, for
// the token eval kept outside this repository. It does nothing in an ordinary
// test run.
func TestEvalRows(t *testing.T) {
	out := os.Getenv("TANDEM_EVAL_OUT")
	if out == "" {
		t.Skip("TANDEM_EVAL_OUT names the file the rows go to")
	}
	pair := os.Getenv("TANDEM_EVAL_PAIR")
	for _, sc := range loadScenarios(t).Scenarios {
		if err := evalrow.Append(out, runEval(t, sc, "durable", pair)); err != nil {
			t.Fatal(err)
		}
	}
}
