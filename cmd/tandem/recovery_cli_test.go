package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

const cliRecoveryPolicy = `{"schema_version":1,"name":"bounded-v1","automatic_repair":{"kinds":["declared_local_check","review_changes_requested"]},` +
	`"max_automatic_repairs_per_work":1,"escalate_to":"planner","replanning":"none"}`

func TestRecoveryStartIsOptInAndRefusesWithoutAnythingCreated(t *testing.T) {
	for name, tc := range map[string]struct {
		policy      string
		collab      bool
		wantUsage   bool
		wantRefusal bool
	}{
		"no collaboration":      {cliRecoveryPolicy, false, true, false},
		"a policy that replans": {strings.Replace(cliRecoveryPolicy, `"none"`, `"automatic"`, 1), true, false, true},
		"a loosened allowlist":  {strings.Replace(cliRecoveryPolicy, `"declared_local_check"`, `"infra"`, 1), true, false, true},
		"two repairs per work":  {strings.Replace(cliRecoveryPolicy, `"max_automatic_repairs_per_work":1`, `"max_automatic_repairs_per_work":2`, 1), true, false, true},
		"not a policy at all":   {"nonsense", true, false, true},
	} {
		e := newCLI(t)
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		args := []string{"--recovery-policy", e.file("recovery.json", tc.policy)}
		if tc.collab {
			args = append(args, "--collaboration", e.createCollaboration())
		}
		rc, _, errOut := e.start(args...)
		switch {
		case tc.wantUsage && rc != exitcode.Usage:
			t.Errorf("%s: exit %d %s", name, rc, errOut)
		case tc.wantRefusal && rc == exitcode.OK:
			t.Errorf("%s: the start was accepted", name)
		}
		if n := e.runCount(); n != 0 {
			t.Errorf("%s: a refused start left %d runs", name, n)
		}
		if _, err := os.Stat(filepath.Join(e.root, ".tandem-relay", "active-run.json")); err == nil {
			t.Errorf("%s: a refused start left an active-run marker", name)
		}
	}
}

func TestRecoveryStartPinsThePolicyInTheRunConfiguration(t *testing.T) {
	for _, withPolicy := range []bool{false, true} {
		e := newCLI(t)
		t.Setenv("TANDEM_BUILD_ENGINE", "durable")
		var args []string
		if withPolicy {
			args = []string{"--collaboration", e.createCollaboration(), "--recovery-policy", e.file("recovery.json", cliRecoveryPolicy)}
		}
		rc, _, errOut := e.start(args...)
		if rc != exitcode.OK {
			t.Fatalf("%d %s", rc, errOut)
		}
		var cfg string
		if err := e.stateDB().QueryRow(`SELECT config_hash FROM runs`).Scan(&cfg); err != nil {
			t.Fatal(err)
		}
		id, _, err := state.LookupIdentity(e.root, e.home)
		if err != nil {
			t.Fatal(err)
		}
		art, err := artifact.Open(id.StateRoot)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := art.ReadAll(context.Background(), cfg, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(raw), `"recovery_policy"`); got != withPolicy {
			t.Errorf("policy flag %v but the pinned configuration has recovery_policy %v: %s", withPolicy, got, raw)
		}
	}
}

func TestReconcileUsageAndRefusals(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if rc, _, _ := e.start(); rc != exitcode.OK {
		t.Fatal("start")
	}
	for name, args := range map[string][]string{"no actor": {"--reason", "x"}, "no reason": {"--actor", "me"}, "neither": {}} {
		if rc, _, _ := e.sub("reconcile", args...); rc != exitcode.Usage {
			t.Errorf("%s: exit %d", name, rc)
		}
	}
	// A run that is not paused by a failure cannot be reconciled, and nothing changes.
	if rc, _, errOut := e.sub("reconcile", "--actor", "me", "--reason", "fixed it"); rc == exitcode.OK || !strings.Contains(errOut, "paused") {
		t.Errorf("reconcile of a running run: %d %s", rc, errOut)
	}
	var n int
	if err := e.stateDB().QueryRow(`SELECT count(*) FROM manual_interventions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused reconcile left %d interventions (%v)", n, err)
	}
	other := newCLI(t)
	if rc, _, _ := other.sub("reconcile", "--actor", "me", "--reason", "x"); rc == exitcode.OK {
		t.Error("reconcile with no durable run")
	}
}
