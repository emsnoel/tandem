package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

func TestStateResumeAccepted(t *testing.T) {
	// Unbound checkout prints nothing and exits 0.
	emptyRoot := initBaselineRepo(t)
	emptyHome := t.TempDir()
	emptyPlan := filepath.Join(emptyRoot, "plan.md")
	if err := os.WriteFile(emptyPlan, []byte("# Plan\n\n### Task 1: A\nstep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out, errOut := runCapture(t, "state", "resume-accepted", "--root", emptyRoot, "--state-home", emptyHome, "--plan", emptyPlan)
	if rc != exitcode.OK || out != "" {
		t.Fatalf("unbound: rc %d out %q err %s", rc, out, errOut)
	}

	// Start a real 3-task run via newCLI so artifacts, tasks and schema are valid.
	e := newCLI(t)
	planText := "# Plan\n\n**Goal:** g.\n\n### Task 1: First\nfirst\n\n### Task 2: Second\nsecond\n\n### Task 3: Third\nthird\n"
	if err := os.WriteFile(e.plan, []byte(planText), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitCmd(e.root, "commit", "-qam", "3 tasks"); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, startOut, errOut := e.start()
	if rc != exitcode.OK {
		t.Fatalf("start: rc %d %s", rc, errOut)
	}
	runID, _ := startOut["run_id"].(string)
	if runID == "" {
		t.Fatalf("missing run_id in %v", startOut)
	}

	id, bound, err := state.LookupIdentity(e.root, e.home)
	if err != nil || !bound {
		t.Fatalf("lookup identity: bound=%v err=%v", bound, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(id.StateRoot, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Record accepted attempts for task-1 and task-2 on the run.
	var snapHash, pktHash string
	if err := db.QueryRow(`SELECT plan_hash, config_hash FROM runs WHERE id=?`, runID).Scan(&snapHash, &pktHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM attempts WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	for i, tid := range []string{"task-1", "task-2"} {
		attID := strings.Repeat(string(rune('1'+i)), 32)
		if _, err := db.Exec(`INSERT INTO attempts (id, run_id, task_id, ordinal, implementation_state, check_state, review_state, budget_state, is_current, start_snapshot_hash, packet_hash, created_ms, accepted_ms) VALUES (?, ?, ?, 1, 'submitted', 'passed', 'passed', 'available', 1, ?, ?, 1, 1)`,
			attID, runID, tid, snapHash, pktHash); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE tasks SET accepted_attempt_id=? WHERE run_id=? AND id=?`, attID, runID, tid); err != nil {
			t.Fatal(err)
		}
	}

	// While run is still running (not archived), latest prints nothing.
	rc, out, errOut = runCapture(t, "state", "resume-accepted", "--root", e.root, "--state-home", e.home, "--plan", e.plan)
	if rc != exitcode.OK || out != "" {
		t.Fatalf("running (not archived): rc %d out %q err %s", rc, out, errOut)
	}

	// Mark the run archived.
	if _, err := db.Exec(`UPDATE runs SET state='archived', archived_ms=2 WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}

	// --from latest returns task-1,task-2.
	rc, out, errOut = runCapture(t, "state", "resume-accepted", "--root", e.root, "--state-home", e.home, "--plan", e.plan, "--from", "latest")
	if rc != exitcode.OK || strings.TrimSpace(out) != "task-1,task-2" {
		t.Fatalf("archived latest: rc %d out %q err %s", rc, out, errOut)
	}

	// Explicit --from <runID> returns task-1,task-2.
	rc, out, errOut = runCapture(t, "state", "resume-accepted", "--root", e.root, "--state-home", e.home, "--plan", e.plan, "--from", runID)
	if rc != exitcode.OK || strings.TrimSpace(out) != "task-1,task-2" {
		t.Fatalf("archived explicit: rc %d out %q err %s", rc, out, errOut)
	}

	// Modified plan (different hash) returns nothing.
	otherPlan := filepath.Join(e.root, "other.md")
	if err := os.WriteFile(otherPlan, []byte(planText+"\nextra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out, errOut = runCapture(t, "state", "resume-accepted", "--root", e.root, "--state-home", e.home, "--plan", otherPlan, "--from", "latest")
	if rc != exitcode.OK || out != "" {
		t.Fatalf("different plan: rc %d out %q err %s", rc, out, errOut)
	}
}
