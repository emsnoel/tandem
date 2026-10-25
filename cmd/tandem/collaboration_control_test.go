package main

import (
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func (e *cliEnv) runState() (string, string) {
	e.t.Helper()
	var st, code string
	if err := e.stateDB().QueryRow(`SELECT state, COALESCE(pause_code,'') FROM runs ORDER BY created_ms DESC LIMIT 1`).Scan(&st, &code); err != nil {
		e.t.Fatal(err)
	}
	return st, code
}

func TestCollaborationHeldLaunchResumesAfterAnAuditedAmendment(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, got, errOut := e.collab("create", "--policy", e.file("enforced.json", `{"enforce_budgets":true,
		"input_ceilings":{"codex":50},"output_ceilings":{"codex":100},"input_reservations":{"codex":60},"output_reservations":{"codex":5}}`))
	if rc != exitcode.OK {
		t.Fatalf("create: %d %s", rc, errOut)
	}
	id := got["collaboration_id"].(string)
	if rc, _, errOut := e.start("--collaboration", id); rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	e.sub("tick")
	if st, code := e.runState(); st != "paused" || code != "scheduling_blocked" {
		t.Fatalf("run %s/%s: a launch over the ceiling holds the run", st, code)
	}
	if rc, _, _ := e.collab("amend", "--collaboration", id, "--provider", "codex", "--input-ceiling", "500"); rc != exitcode.Usage {
		t.Fatalf("an amendment without actor and reason: %d", rc)
	}
	rc, got, errOut = e.collab("amend", "--collaboration", id, "--provider", "codex", "--input-ceiling", "500", "--actor", "user", "--reason", "task one needs room")
	if rc != exitcode.OK || got["run_resumed"] != true {
		t.Fatalf("amend: %d %v %s", rc, got, errOut)
	}
	e.sub("tick")
	var admitted int
	if err := e.stateDB().QueryRow(`SELECT count(*) FROM dispatch_admissions WHERE collaboration_id=?`, id).Scan(&admitted); err != nil || admitted != 1 {
		t.Fatalf("%d admissions after the amendment, %v", admitted, err)
	}
	if st, _ := e.runState(); st != "running" {
		t.Fatalf("run %s after the admitted launch", st)
	}
}

func TestCollaborationStopAndResumeAreAudited(t *testing.T) {
	e := newCLI(t)
	id := e.createCollaboration()
	if rc, _, _ := e.collab("stop", "--collaboration", id); rc != exitcode.Usage {
		t.Fatalf("a stop without actor and reason: %d", rc)
	}
	for _, verb := range []string{"stop", "resume"} {
		if rc, _, errOut := e.collab(verb, "--collaboration", id, "--actor", "user", "--reason", verb+" for the test"); rc != exitcode.OK {
			t.Fatalf("%s: %d %s", verb, rc, errOut)
		}
	}
	var audits int
	if err := e.stateDB().QueryRow(`SELECT count(*) FROM collaboration_audit WHERE collaboration_id=? AND actor='user'`, id).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("%d audit rows, %v", audits, err)
	}
}
