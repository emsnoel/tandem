package main

import (
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestCollaborationCollectCloseReportEndToEnd(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	id := e.createCollaboration()
	if rc, _, errOut := e.start("--collaboration", id); rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	e.sub("tick") // the fake transport reports session "cli" for the builder
	sessions := t.TempDir()

	// Before any log exists: partial, with the reason, and no invented zero.
	rc, got, errOut := e.collab("collect", "--collaboration", id, "--sessions-dir", sessions, "--json")
	if rc != exitcode.OK {
		t.Fatalf("collect: %d %s", rc, errOut)
	}
	snap := got["snapshot"].(map[string]any)
	if snap["complete"] != false || len(snap["totals"].(map[string]any)) != 0 {
		t.Fatalf("a missing log: %v", snap)
	}

	codexLog(t, sessions, "cli", "", totalEv(100, 40, 5), totalEv(150, 60, 8),
		`{"type":"event_msg","payload":{"type":"task_complete"}}`)
	rc, got, errOut = e.collab("collect", "--collaboration", id, "--sessions-dir", sessions, "--json")
	if rc != exitcode.OK {
		t.Fatalf("collect: %d %s", rc, errOut)
	}
	codex := got["snapshot"].(map[string]any)["totals"].(map[string]any)["codex"].(map[string]any)
	if codex["input_total"] != float64(150) || codex["cache_read"] != float64(60) || codex["output"] != float64(8) {
		t.Fatalf("codex totals %v", codex)
	}

	// Close freezes launches; the builder's job is still running, so it stays closing.
	rc, got, errOut = e.collab("close", "--collaboration", id, "--sessions-dir", sessions, "--json")
	if rc != exitcode.OK {
		t.Fatalf("close: %d %s", rc, errOut)
	}
	if got["snapshot"].(map[string]any)["status"] != "closing" || got["snapshot"].(map[string]any)["complete"] != false {
		t.Fatalf("close with an unsettled builder: %v", got["snapshot"])
	}
	var status string
	if err := e.stateDB().QueryRow(`SELECT status FROM collaborations WHERE id=?`, id).Scan(&status); err != nil || status != "closing" {
		t.Fatalf("status %q %v", status, err)
	}
	// Report reads the stored snapshot only: it needs no logs at all.
	rc, got, _ = e.collab("report", "--collaboration", id)
	if rc != exitcode.OK || got["record"].(map[string]any)["revision"] == nil {
		t.Fatalf("report: %d %v", rc, got)
	}
	if rc, _, _ := e.collab("report", "--collaboration", "000000000000000000000000000000dd"); rc == exitcode.OK {
		t.Fatal("a report for an unknown collaboration succeeded")
	}
	if rc, _, _ := e.collab("collect"); rc != exitcode.Usage {
		t.Fatalf("collect without --collaboration: %d", rc)
	}
}
