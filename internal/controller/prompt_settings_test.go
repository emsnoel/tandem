package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	testRuling    = "I commit in this repo, not you; skip the commit steps"
	testObjection = "1. Task 2 commits.\n2. Task 3 commits too."
	testExtra     = "Never touch the vendor directory."
)

func withSettings(r *StartRequest) {
	r.ConventionsFile, r.PromptExtra = "CONVENTIONS.md", testExtra
	r.Ruling, r.RulingObjection = testRuling, testObjection
}

// The wording is the legacy prompt's, kept as it was.
func TestTaskPromptCarriesTheProjectSettingsInTheOldWording(t *testing.T) {
	e := newCEnv(t)
	run, err := e.c.Start(ctx, e.startRequest(withSettings))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = run
	prompt, _ := e.packet(e.current("task-1"))
	// The run resolves its root, so only what follows it is compared.
	prelude := "/CONVENTIONS.md in full before you start. It carries\nconstraints you cannot infer from the code."
	overruled := "OVERRULED OBJECTIONS\nYou raised these against this plan and the planner has ruled. These rulings\n" +
		"outrank the plan - they are the only part of this message that does. Do not\nraise them again.\n\n" +
		"  1. Task 2 commits.\n  2. Task 3 commits too.\n\n  RULING: " + testRuling
	order := []string{"Implement task 1 of 2", "Also read ", prelude, "Your task:", overruled, testExtra, "BEFORE YOU EDIT ANYTHING"}
	at := -1
	for _, want := range order {
		i := strings.Index(prompt, want)
		if i < 0 {
			t.Fatalf("the prompt is missing %q:\n%s", want, prompt)
		}
		if i < at {
			t.Errorf("%q is out of order:\n%s", want, prompt)
		}
		at = i
	}
}

func TestARepairPromptCarriesTheSettingsToo(t *testing.T) {
	cfg := runConfig{Root: "/r", PlanPath: "/r/p.md", Settings: &promptSettings{Conventions: "C.md", PromptExtra: testExtra, Ruling: testRuling}}
	spec := workflow.TaskSpec{ID: "task-1", Brief: "Do one."}
	req := packetRequest(cfg, "r", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 32), 1, 1, spec, []memory.Entry(nil), "The reviewer asked for changes.")
	text := req.Header + req.Footer
	for _, want := range []string{"Also read /r/C.md", "RULING: " + testRuling, testExtra, "The reviewer asked for changes."} {
		if !strings.Contains(text, want) {
			t.Errorf("the repair prompt is missing %q:\n%s", want, text)
		}
	}
}

func TestAPromptWithoutSettingsIsUnchanged(t *testing.T) {
	e := newCEnv(t)
	e.start()
	prompt, _ := e.packet(e.current("task-1"))
	for _, absent := range []string{"Also read", "OVERRULED", "RULING:"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("a run with no settings has %q:\n%s", absent, prompt)
		}
	}
	run, _ := e.st.GetRun(ctx, e.runID)
	raw, err := e.c.d.Artifacts.ReadAll(ctx, run.ConfigHash, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "settings") {
		t.Errorf("a run with no settings stores them anyway, which changes its config hash: %s", raw)
	}
}

func TestAConventionsFileTheBuilderLoadsIsNotAskedFor(t *testing.T) {
	e := newCEnv(t) // a codex builder, which loads AGENTS.md itself
	run, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) { r.ConventionsFile = "AGENTS.md" }))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = run
	if prompt, _ := e.packet(e.current("task-1")); strings.Contains(prompt, "Also read") {
		t.Errorf("the builder is asked to read a file its CLI loads:\n%s", prompt)
	}
}

func TestPromptExtraCountsTowardTheContextLimit(t *testing.T) {
	e := newCEnv(t)
	_, err := e.c.Start(ctx, e.startRequest(func(r *StartRequest) {
		r.ContextLimit = taskcontext.DefaultMaxEstimatedTokens
		r.PromptExtra = strings.Repeat("x", taskcontext.DefaultMaxEstimatedTokens)
	}))
	if !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Fatalf("an oversized prompt_extra started: %v", err)
	}
}

func TestTheDryRunMeasuresTheSettingsToo(t *testing.T) {
	e := newCEnv(t)
	err := CheckTaskContext(e.startRequest(func(r *StartRequest) {
		r.PromptExtra = strings.Repeat("x", taskcontext.DefaultMaxEstimatedTokens)
	}))
	if !errors.Is(err, workflow.ErrContextTooLarge) {
		t.Fatalf("a dry run passed an oversized prompt_extra: %v", err)
	}
}
