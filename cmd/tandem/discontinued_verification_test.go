package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// settledTransport is the older tandem's view while it drove the run: every
// send lands and every pane has finished.
type settledTransport struct{}

func (settledTransport) Send(_ context.Context, d runner.Delivery) (runner.Receipt, error) {
	return runner.Receipt{EffectID: d.EffectID, ExternalID: "seed"}, nil
}
func (settledTransport) Observe(context.Context, string) (runner.Observation, error) {
	return runner.Observation{State: runner.ObsCompleted}, nil
}
func (settledTransport) ObserveAgent(context.Context, string) (runner.Observation, error) {
	return runner.Observation{State: runner.ObsUnknown}, nil
}

// seedStagedVerification drives a run with supported kinds, as an older tandem
// did, until the planner's verification is staged and waiting for the planner's
// turn to end. It then changes the stored builder kind to a discontinued one,
// and returns the run and the candidate's job.
func seedStagedVerification(t *testing.T, e *cliEnv) (string, string) {
	t.Helper()
	ctx := context.Background()
	id, err := state.ResolveIdentity(e.root, e.home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(ctx, id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) string {
		h, err := art.Put(ctx, bytesReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AdoptArtifact(ctx, h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	const collab = "00000000000000000000000000c0ab01"
	if err := st.CreateCollaboration(ctx, state.CollaborationRequest{ID: collab, PolicyHash: put(`{"review":"handback"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSource(ctx, state.SourceRequest{ID: "00000000000000000000000000c0ab02", CollaborationID: collab, Provider: "claude",
		Role: "planner", SessionID: "planner-claude-session"}); err != nil {
		t.Fatal(err)
	}
	c, err := controller.New(controller.Dependencies{State: st, Artifacts: art, Transport: settledTransport{}, CheckExecutor: controller.NewCheckExecutor(art), Owner: cliOwner})
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.Start(ctx, controller.StartRequest{Root: e.root, PlanPath: e.plan, BuilderKind: "codex", BuilderSession: "builder", PlannerSession: "planner",
		CheckCommand: "true", ReviewerKind: "claude", ReviewerSession: "reviewer", Collaboration: collab, ReviewProtocol: 3, ReviewDelivery: "mailbox",
		AcceptancePolicyPath: e.file("policy.json", handbackPolicy)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(ctx, run); err != nil {
		t.Fatal(err)
	}
	a := e.attempt("task-1")
	n := 0
	send := func(version int, job, effect, nonce, kind, payload string) {
		t.Helper()
		n++
		name := fmt.Sprintf("payloads/seed-%d.json", n)
		dir := filepath.Join(paths.Relay(e.root), "durable", run)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(payload))
		raw, err := json.Marshal(controller.Envelope{SchemaVersion: version, EventID: fmt.Sprintf("%032x", 0xD000+n), RunID: run, TaskID: "task-1",
			AttemptID: a.attempt, JobID: job, EffectID: effect, Nonce: nonce, Kind: kind, PayloadPath: name, PayloadHash: hex.EncodeToString(sum[:])})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Ingest(ctx, raw); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	send(2, a.job, a.effect, a.nonce, workflow.EventBuilderStarted, "{}\n")
	os.WriteFile(filepath.Join(e.root, "work.txt"), []byte("work\n"), 0o644)
	send(2, a.job, a.effect, a.nonce, workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")
	// until ticks the run, up to a limit, until an effect of this kind is delivered.
	until := func(kind string) workflow.Effect {
		t.Helper()
		for i := 0; i < 80; i++ {
			if err := c.Tick(ctx, run); err != nil {
				t.Fatalf("waiting for %s: %v", kind, err)
			}
			effects, err := st.RunEffects(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range effects {
				if x.Kind == kind && x.State == "delivering" {
					return x
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("no %s was delivered", kind)
		return workflow.Effect{}
	}
	until("review_prompt")
	if err := c.Waive(ctx, run, "task-1", "review", "the planner verifies", "tester"); err != nil {
		t.Fatal(err)
	}
	prompt := until("verify_prompt")
	cur, err := st.GetAttempt(ctx, a.attempt)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(workflow.ReviewResult{SchemaVersion: 1, JobID: prompt.JobID, AttemptID: a.attempt, PatchHash: cur.PatchHash, TreeHash: cur.EndSnapshotHash,
		Status: workflow.ReviewStatusPassed, Findings: []workflow.Finding{}})
	send(3, prompt.JobID, prompt.ID, prompt.Nonce, "review_started", "{\"session_id\":\"planner-conversation\"}\n")
	send(3, prompt.JobID, prompt.ID, prompt.Nonce, "review_result", string(result))
	if cand, found, err := st.ReviewCandidateFor(ctx, prompt.JobID); err != nil || !found || cand.State != "staged" {
		t.Fatalf("candidate %+v %v %v", cand, found, err)
	}

	raw, err := art.ReadAll(ctx, mustRun(t, st, run).ConfigHash, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["builder_kind"] = "opencode"
	pinned, _ := json.Marshal(cfg)
	db := e.stateDBWritable()
	if _, err := db.Exec(`UPDATE runs SET config_hash=? WHERE id=?`, put(string(pinned)), run); err != nil {
		t.Fatal(err)
	}
	return run, prompt.JobID
}

func mustRun(t *testing.T, st *state.Store, id string) state.Run {
	t.Helper()
	run, err := st.GetRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// The builder's pane is idle and the planner is still in its turn. The planner's
// staged verification waits for the planner, not the builder.
func TestDiscontinuedRunJudgesAVerificationByThePlannersPane(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	_, job := seedStagedVerification(t, e)

	old := closingPanes
	planner := "working"
	closingPanes = func(name string) (*herdr.Agent, error) {
		if name == "planner" {
			return newPane(planner).read(name)
		}
		return newPane("idle").read(name)
	}
	t.Cleanup(func() { closingPanes = old })

	if rc, out, errOut := e.sub("tick"); rc != exitcode.OK {
		t.Fatalf("tick rc %d %s %s", rc, out, errOut)
	}
	var got string
	if err := e.stateDB().QueryRow(`SELECT state FROM review_candidates WHERE job_id=?`, job).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "staged" {
		t.Fatalf("the builder's idle pane settled a verification the planner is still working on: candidate %s", got)
	}
}

func bytesReader(s string) *strings.Reader { return strings.NewReader(s) }

func (e *cliEnv) stateDBWritable() *sql.DB {
	e.t.Helper()
	id, bound, err := state.LookupIdentity(e.root, e.home)
	if err != nil || !bound {
		e.t.Fatalf("identity %v %v", bound, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(id.StateRoot, "state.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	return db
}
