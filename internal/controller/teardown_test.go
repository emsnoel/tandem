package controller

import (
	"reflect"
	"testing"
)

func unmetByTask(ev RunEvidence) map[string][]string {
	out := map[string][]string{}
	for _, t := range ev.Tasks {
		out[t.ID] = t.Unmet
	}
	return out
}

func TestTeardownEvidenceOfAFinishedRunIsComplete(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.passTask(1, 1)
	e.passTask(2, 0)
	ev, err := e.c.TeardownEvidence(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.RunID != e.runID || ev.State != "finished" || len(ev.Tasks) != 2 {
		t.Fatalf("%+v", ev)
	}
	for _, task := range ev.Tasks {
		if len(task.Unmet) != 0 {
			t.Errorf("task %s of a finished run lacks %v", task.ID, task.Unmet)
		}
	}
}

func TestTeardownEvidenceNamesWhatEachTaskLacks(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.passTask(1, 0)
	e.tick() // task 2 is sent and not yet handed back
	ev, err := e.c.TeardownEvidence(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"task-1": nil, "task-2": {"acceptance"}}
	if got := unmetByTask(ev); !reflect.DeepEqual(got, want) {
		t.Fatalf("unmet %v, want %v", got, want)
	}
	if ev.State != "running" {
		t.Errorf("state %s", ev.State)
	}
}
