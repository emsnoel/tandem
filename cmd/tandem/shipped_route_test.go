package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// A run started before every run needed a reviewer has none in its stored
// configuration. On the shipped transport it must still follow its note to
// archived. seedRun seeds a stored kind, so the reviewer is cleared after it.
func TestNoReviewerRunFollowsItsNoteToArchivedOnTheShippedTransport(t *testing.T) {
	for name, keep := range map[string]bool{"prompt acknowledged": true, "prompt open": false} {
		t.Run(name, func(t *testing.T) {
			e := upgradedCLI(t, newPane("idle").read)
			run := seedRun(t, e, "codex", "claude", false, func(st *state.Store, eff workflow.Effect) error {
				if keep {
					return st.AcknowledgeEffect(context.Background(), eff.ID, eff.Nonce)
				}
				return nil
			})
			clearReviewer(t, e, run)
			if ag := e.agentsOf(run); ag.ReviewerKind != "" || ag.ReviewerSession != "" {
				t.Fatalf("setup: %+v", ag)
			}
			if rc, out, errOut := e.sub("tick"); rc != exitcode.OK {
				t.Fatalf("tick: %d %s %s", rc, out, errOut)
			}
			if st, code := e.runState(); st != "paused" || code != "no_reviewer" {
				t.Fatalf("the run is %s/%s", st, code)
			}
			e.stopAndWait(exitcode.OK)
			if rc, out, errOut := e.state("recover", "--run", run); rc != exitcode.OK {
				t.Fatalf("recover: %d %s %s", rc, out, errOut)
			}
			if !keep {
				eff := e.attempt("task-1").effect
				if rc, out, errOut := e.state("settle-delivery", "--run", run, "--effect", eff, "--abandon", "--because", "never shown"); rc != exitcode.OK {
					t.Fatalf("settle: %d %s %s", rc, out, errOut)
				}
			}
			if rc, out, errOut := e.state("archive", "--run", run); rc != exitcode.OK {
				t.Fatalf("archive: %d %s %s", rc, out, errOut)
			}
			if st, _ := e.runState(); st != "archived" {
				t.Fatalf("the run is %s", st)
			}
		})
	}
}

// fakeHerdrBin puts a herdr on PATH whose panes are all present and idle and
// that types nothing, so the shipped transport is what the commands really build.
func fakeHerdrBin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
  "agent get") printf '{"result":{"agent":{"agent":"codex","agent_status":"idle","name":"%s","pane_id":"w1:%s","state_change_seq":1}}}\n' "$3" "$3" ;;
  *) printf '{"error":{"code":"unsupported","message":"not faked"}}\n' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Every pause code, through the commands the notes print, on the transport the
// binary really builds. TestEveryPauseHasAWayOut injects a fake transport, so it
// cannot see a run the shipped transport refuses to build.
func TestEveryPauseLeavesThroughTheShippedTransport(t *testing.T) {
	for _, code := range controller.PauseCodes() {
		if code == workflow.PauseStopping {
			continue
		}
		t.Run(code, func(t *testing.T) {
			fakeHerdrBin(t)
			e := upgradedCLI(t, newPane("idle").read)
			builder := "codex"
			if code == "agent_unsupported" {
				builder = "opencode"
			}
			run := seedRun(t, e, builder, "claude", false, func(*state.Store, workflow.Effect) error { return nil })
			if code == "no_reviewer" {
				clearReviewer(t, e, run)
			}
			if code != "stopped" {
				if _, err := e.stateDBWritable().Exec(`UPDATE runs SET state='paused', pause_code=? WHERE id=?`, code, run); err != nil {
					t.Fatal(err)
				}
			}
			e.stopAndWait(exitcode.OK)
			if st, got := e.runState(); st != "paused" || got != "stopped" {
				t.Fatalf("after the stop the run is %s/%s", st, got)
			}
			if rc, out, errOut := e.state("recover", "--run", run); rc != exitcode.OK {
				t.Fatalf("recover: %d %s %s", rc, out, errOut)
			}
			rc, _, errOut := e.state("archive", "--run", run)
			if m := settleCommand.FindStringSubmatch(errOut); m != nil {
				args := strings.Fields(strings.NewReplacer("WHY", "gave-up", "DIR", e.root).Replace(m[1]))
				if rc, out, errOut := e.run(append(args, "--state-home", e.home)...); rc != exitcode.OK {
					t.Fatalf("%v: %d %s %s", args, rc, out, errOut)
				}
				rc, _, errOut = e.state("archive", "--run", run)
			}
			if rc != exitcode.OK {
				t.Fatalf("archive: %d %s", rc, errOut)
			}
			if st, _ := e.runState(); st != "archived" {
				t.Fatalf("the run is %s", st)
			}
		})
	}
}

func (e *cliEnv) agentsOf(run string) controller.Agents {
	e.t.Helper()
	id, err := state.ResolveIdentity(e.root, e.home)
	if err != nil {
		e.t.Fatal(err)
	}
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		e.t.Fatal(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		e.t.Fatal(err)
	}
	ag, err := controller.RunAgents(context.Background(), st, art, run)
	if err != nil {
		e.t.Fatal(err)
	}
	return ag
}

// clearReviewer rewrites the run's stored configuration without a reviewer.
func clearReviewer(t *testing.T, e *cliEnv, run string) {
	t.Helper()
	id, err := state.ResolveIdentity(e.root, e.home)
	if err != nil {
		t.Fatal(err)
	}
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	db := e.stateDBWritable()
	var hash string
	if err := db.QueryRow(`SELECT config_hash FROM runs WHERE id=?`, run).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	raw, err := art.ReadAll(context.Background(), hash, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	delete(cfg, "reviewer_kind")
	delete(cfg, "reviewer_session")
	pinned, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	next, err := art.Put(context.Background(), bytes.NewReader(pinned))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,'application/json',1)`, next, len(pinned)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET config_hash=? WHERE id=?`, next, run); err != nil {
		t.Fatal(err)
	}
}
