package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// The states a typed prompt can be left in when the builder never acknowledged
// it. pausedUnknown is how an older tandem's tick left a delivery_unknown run.
var openDispatches = map[string]func(*state.Store, workflow.Effect) error{
	"delivering": func(*state.Store, workflow.Effect) error { return nil },
	"delivery_unknown": func(st *state.Store, eff workflow.Effect) error {
		return st.MarkDeliveryUnknown(context.Background(), eff.ID, eff.Fence, "")
	},
	"delivery_unknown, paused writer_unknown": func(st *state.Store, eff workflow.Effect) error {
		if err := st.MarkDeliveryUnknown(context.Background(), eff.ID, eff.Fence, ""); err != nil {
			return err
		}
		return st.SetRunState(context.Background(), eff.RunID, "paused", "writer_unknown")
	},
}

func (e *cliEnv) effectState(id string) string {
	e.t.Helper()
	var got string
	if err := e.stateDB().QueryRow(`SELECT state FROM effects WHERE id=?`, id).Scan(&got); err != nil {
		e.t.Fatal(err)
	}
	return got
}

// A discontinued builder's pane can never return its nonce, so its open prompt
// cannot hold the archive.
func TestDiscontinuedBuildersOpenDispatchEndsArchived(t *testing.T) {
	panes := map[string]func(string) (*herdr.Agent, error){"idle": newPane("idle").read, "gone": gonePane}
	for open, after := range openDispatches {
		for pane, get := range panes {
			t.Run(open+", pane "+pane, func(t *testing.T) {
				e := upgradedCLI(t, get)
				run := seedRun(t, e, "opencode", "claude", false, after)
				a := e.attempt("task-1")
				sent := e.tr.count()
				if rc, out, errOut := e.sub("tick"); rc != exitcode.OK {
					t.Fatalf("tick rc %d %s %s", rc, out, errOut)
				}
				closeRun(t, e, run)
				if got, _ := e.runState(); got != "archived" {
					t.Fatalf("run is %s", got)
				}
				if got := e.effectState(a.effect); got != "cancelled" {
					t.Fatalf("the prompt is %s", got)
				}
				if n := e.tr.count(); n != sent {
					t.Fatalf("%d prompts typed after the upgrade", n-sent)
				}
			})
		}
	}
}

// A supported builder in a mixed run can still return its nonce, so its open
// prompt keeps the archive gate and its message.
func TestMixedRunKeepsTheGateOnASupportedBuildersOpenDispatch(t *testing.T) {
	for open, after := range openDispatches {
		t.Run(open, func(t *testing.T) {
			e := upgradedCLI(t, newPane("idle").read)
			run := seedRun(t, e, "codex", "opencode", false, after)
			a := e.attempt("task-1")
			want := e.effectState(a.effect)
			for _, step := range [][]string{{"build", "tick"}, {"build", "stop"}, {"build", "tick"}, {"state", "recover", "--run", run}} {
				if rc, out, errOut := e.run(append(step, "--root", e.root, "--state-home", e.home)...); rc != exitcode.OK {
					t.Fatalf("%v rc %d %s %s", step, rc, out, errOut)
				}
			}
			rc, _, errOut := e.run("state", "archive", "--run", run, "--root", e.root, "--state-home", e.home)
			if rc != exitcode.ManualGate || !strings.Contains(errOut, "has 1 delivery whose outcome is open") {
				t.Fatalf("archive rc %d %q", rc, errOut)
			}
			if got := e.effectState(a.effect); got != want {
				t.Fatalf("the prompt moved from %s to %s", want, got)
			}
		})
	}
}

// queueHandback writes what a builder queued before the upgrade: its submission,
// with the nonce given.
func queueHandback(t *testing.T, e *cliEnv, a attemptInfo, nonce string) string {
	t.Helper()
	dir := filepath.Join(paths.Relay(e.root), "durable", a.run)
	payload := "{\"message\":\"done\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "payloads", "submit.json"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	raw, err := json.Marshal(controller.Envelope{SchemaVersion: 2, EventID: fmt.Sprintf("%032x", 0xB001), RunID: a.run, TaskID: "task-1",
		AttemptID: a.attempt, JobID: a.job, EffectID: a.effect, Nonce: nonce, Kind: workflow.EventBuilderSubmitted,
		PayloadPath: "payloads/submit.json", PayloadHash: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "q", "submit.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Observing a real pane means a tick can now ingest a queued handback. Whatever
// becomes of it, the run still pauses and nothing more reaches a pane.
func TestQueuedHandbackOnADiscontinuedRun(t *testing.T) {
	// receipts counts the prompts the store records as delivered: only the
	// seed's. A refused send records none.
	receipts := func(e *cliEnv, run string) int {
		t.Helper()
		var n int
		if err := e.stateDB().QueryRow(`SELECT count(*) FROM effects WHERE run_id=? AND receipt_hash IS NOT NULL`, run).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	implementation := func(e *cliEnv, attempt string) string {
		t.Helper()
		var got string
		if err := e.stateDB().QueryRow(`SELECT implementation_state FROM attempts WHERE id=?`, attempt).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	tickPaused := func(t *testing.T, e *cliEnv) {
		t.Helper()
		rc, out, errOut := e.sub("tick")
		if res := oneJSON(t, out); rc != exitcode.OK || res["state"] != "paused" || res["pause_code"] != "agent_unsupported" {
			t.Fatalf("tick rc %d %s %s", rc, out, errOut)
		}
	}

	t.Run("a valid submission is applied", func(t *testing.T) {
		e := upgradedCLI(t, newPane("idle").read)
		run := seedDispatchedRun(t, e, "opencode", "claude", false)
		a := e.attempt("task-1")
		sent := e.tr.count()
		os.WriteFile(filepath.Join(e.root, "work.txt"), []byte("work\n"), 0o644)
		queueHandback(t, e, a, a.nonce)

		tickPaused(t, e)
		if got := implementation(e, a.attempt); got != "submitted" {
			t.Fatalf("the attempt is %s", got)
		}
		if q := queued(e, run); len(q) != 0 {
			t.Fatalf("still queued: %v", q)
		}
		closeRun(t, e, run)
		if n := e.tr.count(); n != sent {
			t.Fatalf("%d prompts typed", n-sent)
		}
		if n := receipts(e, run); n != 1 {
			t.Fatalf("%d effects delivered, want only the seed's", n)
		}
	})

	t.Run("a forged submission is quarantined", func(t *testing.T) {
		e := upgradedCLI(t, newPane("idle").read)
		run := seedDispatchedRun(t, e, "opencode", "claude", false)
		a := e.attempt("task-1")
		sent := e.tr.count()
		dir := queueHandback(t, e, a, strings.Repeat("0", 32))

		tickPaused(t, e)
		if got := implementation(e, a.attempt); got == "submitted" {
			t.Fatal("a forged submission was applied")
		}
		if m, _ := filepath.Glob(filepath.Join(dir, "quarantine", "submit.json*")); len(m) == 0 {
			t.Fatal("nothing was quarantined")
		}
		closeRun(t, e, run)
		if n := e.tr.count(); n != sent {
			t.Fatalf("%d prompts typed", n-sent)
		}
		if n := receipts(e, run); n != 1 {
			t.Fatalf("%d effects delivered, want only the seed's", n)
		}
	})
}
