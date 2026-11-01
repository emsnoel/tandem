package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

// nulFields splits what a --fields verb printed.
func nulFields(t *testing.T, out string) []string {
	t.Helper()
	if !strings.HasSuffix(out, "\x00") {
		t.Fatalf("output does not end in NUL: %q", out)
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
}

func TestIngestFields(t *testing.T) {
	d := durableRepo(t)
	_, got, _ := d.newEvent(t, "builder_started")
	taken := claim(t, got["queue_file"].(string))
	rc, out, errOut := runCapture(t, "build", "event", "ingest", "--root", d.root, "--state-home", d.home, "--claimed", taken, "--fields")
	if f := nulFields(t, out); rc != exitcode.OK || !reflect.DeepEqual(f, []string{"committed", ""}) {
		t.Fatalf("rc %d, fields %q, stderr %s", rc, f, errOut)
	}
}

func TestIngestFieldsQuarantined(t *testing.T) {
	d := durableRepo(t)
	forged := d.dir + "/q/forged.json.taken"
	os.WriteFile(forged, []byte(`{"schema_version":2,"kind":"accept_requested"}`), 0o644)
	rc, out, _ := runCapture(t, "build", "event", "ingest", "--root", d.root, "--state-home", d.home, "--claimed", forged, "--fields")
	if f := nulFields(t, out); rc != exitcode.OK || len(f) != 2 || f[0] != "quarantined" || f[1] == "" {
		t.Fatalf("rc %d, fields %q", rc, f)
	}
}

// Field order: run_id, state, pause_code, skipped.
func TestTickFieldsRunning(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, started, _ := e.start()
	run := started["run_id"].(string)
	for _, verb := range []string{"tick", "recover"} {
		rc, out, errOut := e.sub(verb, "--fields")
		if f := nulFields(t, out); rc != exitcode.OK || !reflect.DeepEqual(f, []string{run, "running", "", ""}) {
			t.Fatalf("%s: rc %d, fields %q, stderr %s", verb, rc, f, errOut)
		}
	}
}

func TestTickFieldsBusy(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, started, _ := e.start()
	run := started["run_id"].(string)
	id, _, _ := state.LookupIdentity(e.root, e.home)
	lk, err := state.OpenLockerNamed(id.StateRoot, "tick-"+run)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lk.TryLockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	rc, out, _ := e.sub("tick", "--fields")
	if f := nulFields(t, out); rc != exitcode.OK || !reflect.DeepEqual(f, []string{run, "", "", "busy"}) {
		t.Fatalf("rc %d, fields %q", rc, f)
	}
}

// A paused run's recover exits ManualGate and still prints its fields.
func TestTickFieldsPaused(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, started, _ := e.start()
	run := started["run_id"].(string)
	e.sub("tick")
	os.WriteFile(e.plan, []byte(cliPlan+"\n### Task 3: Third\n\nthird\n"), 0o644)
	rc, out, _ := e.sub("recover", "--fields")
	if f := nulFields(t, out); rc != exitcode.ManualGate || !reflect.DeepEqual(f, []string{run, "paused", "plan_changed", ""}) {
		t.Fatalf("rc %d, fields %q", rc, f)
	}
}

// Field order: state, pause_code, dispatch, pause_reason, as build wait.
func TestStatusFieldsMatchWait(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	e.start()
	e.sub("tick")
	os.WriteFile(e.plan, []byte(cliPlan+"\n### Task 3: Third\n\nthird\n"), 0o644)
	e.sub("recover")
	rc, status, errOut := e.sub("status", "--fields")
	if rc != exitcode.OK {
		t.Fatalf("status rc %d %s", rc, errOut)
	}
	_, wait, _ := e.sub("wait", "--until", "paused", "--timeout", "0", "--fields")
	if status != wait {
		t.Fatalf("status %q, wait %q", status, wait)
	}
	if f := nulFields(t, status); len(f) != 4 || f[0] != "paused" || f[1] != "plan_changed" {
		t.Fatalf("fields %q", f)
	}
}
