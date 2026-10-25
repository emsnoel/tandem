package state

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

var ctx = context.Background()

func newSeededStore(t *testing.T) (*Store, *seed, string) {
	t.Helper()
	root := newRoot(t)
	s := openStore(t, root)
	s.now = fixedClock()
	return s, seedProjectRunTaskAttempt(t, s, 1), root
}

func (sd *seed) event(id int, kind string, version int64) workflow.Event {
	return workflow.Event{ID: hexID(5000 + id), RunID: sd.RunID, AttemptID: sd.AttemptID, Kind: kind,
		PayloadHash: sd.PayloadHash, ExpectedVersion: version}
}

var owner = workflow.Observed{WriterOwned: true, SchedulingAllowed: true}

func mustApply(t *testing.T, s *Store, ev workflow.Event, obs workflow.Observed) workflow.Receipt {
	t.Helper()
	r, err := s.ApplyWith(ctx, ev, obs)
	if err != nil {
		t.Fatalf("Apply %s: %v", ev.Kind, err)
	}
	return r
}

// rows counts what an event leaves behind, so "nothing happened" is a number.
func (sd *seed) rows(t *testing.T) [4]int {
	return [4]int{sd.count(t, "events"), sd.count(t, "effects"), sd.count(t, "artifact_refs"), int(sd.version(t))}
}

func TestApplyAttemptCreatedWritesEventAttemptAndEffectTogether(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	ev := sd.event(1, workflow.EventAttemptCreated, 0)
	r := mustApply(t, s, ev, owner)
	if r.Duplicate || r.Version != 1 || r.EventID != ev.ID {
		t.Fatalf("receipt %+v", r)
	}
	if got := sd.rows(t); got != [4]int{1, 1, 2, 1} {
		t.Fatalf("events, effects, refs, version = %v", got)
	}
	var kind, state, payload, nonce, source string
	var fence, ordinal int
	err := s.db.QueryRow(`SELECT id,kind,state,payload_hash,nonce,fence,ordinal FROM effects WHERE source_event_id=?`, ev.ID).
		Scan(&source, &kind, &state, &payload, &nonce, &fence, &ordinal)
	if err != nil {
		t.Fatal(err)
	}
	if source != effectID(ev.ID, 0) || kind != workflow.EffectBuilderDispatch || state != "pending" || payload != sd.PacketHash ||
		fence != 0 || ordinal != 0 || len(nonce) != 32 || nonce != effectNonce(ev.ID, 0) {
		t.Fatalf("effect id %s kind %s state %s payload %s nonce %s fence %d ordinal %d", source, kind, state, payload, nonce, fence, ordinal)
	}
	var applied int64
	s.db.QueryRow(`SELECT applied_version FROM events WHERE id=?`, ev.ID).Scan(&applied)
	if applied != 1 {
		t.Fatalf("stored applied_version %d", applied)
	}
}

func TestApplyDuplicateReturnsOriginalReceipt(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	created := sd.event(1, workflow.EventAttemptCreated, 0)
	first := mustApply(t, s, created, owner)
	mustApply(t, s, sd.event(2, workflow.EventBuilderStarted, 1), owner) // the version advances to 2
	before := sd.rows(t)

	// The replay still carries the stale expected version 0.
	again, err := s.ApplyWith(ctx, created, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.Version != first.Version || again.Version != 1 {
		t.Fatalf("duplicate receipt %+v, original %+v", again, first)
	}
	if sd.rows(t) != before {
		t.Fatalf("a duplicate changed the workflow: %v -> %v", before, sd.rows(t))
	}
	// Nothing about the replay needs the writer: a duplicate is answered first.
	if _, err := s.Apply(ctx, created); err != nil {
		t.Fatalf("duplicate without controller facts: %v", err)
	}
}

func TestApplyConflictingDuplicateRefused(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	created := sd.event(1, workflow.EventAttemptCreated, 0)
	mustApply(t, s, created, owner)
	before := sd.rows(t)
	other := sd.artifact(t, "a different payload")
	for name, mutate := range map[string]func(*workflow.Event){
		"payload": func(e *workflow.Event) { e.PayloadHash = other },
		"kind":    func(e *workflow.Event) { e.Kind = workflow.EventBuilderStarted },
		"attempt": func(e *workflow.Event) { e.AttemptID = hexID(999) },
		"run":     func(e *workflow.Event) { e.RunID = hexID(998) },
	} {
		ev := created
		mutate(&ev)
		if _, err := s.ApplyWith(ctx, ev, owner); !errors.Is(err, workflow.ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", name, err)
		}
	}
	if sd.rows(t) != before {
		t.Fatalf("a refused duplicate added rows: %v -> %v", before, sd.rows(t))
	}
}

func TestApplyRejectsSupersededAttempt(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	sd.submitReady(t)
	sd.setAttempt(t, `implementation_state='submitted', check_state='passed', review_state='changes_requested'`)
	v := sd.version(t)
	r := mustApply(t, s, sd.event(1, workflow.EventRepairStarted, v), owner)
	if r.Version != v+1 {
		t.Fatalf("repair receipt %+v", r)
	}
	var current int
	s.db.QueryRow(`SELECT is_current FROM attempts WHERE id=?`, sd.AttemptID).Scan(&current)
	if current != 0 {
		t.Fatal("the repaired attempt is still current")
	}
	// A valid event for the old attempt, carrying the version it was issued at.
	before := sd.rows(t)
	for _, kind := range []string{workflow.EventBuilderStarted, workflow.EventBuilderStuck, workflow.EventCheckCompleted} {
		if _, err := s.ApplyWith(ctx, sd.event(10, kind, r.Version), owner); !errors.Is(err, workflow.ErrStaleAttempt) {
			t.Errorf("%s after repair: err = %v, want ErrStaleAttempt", kind, err)
		}
	}
	if sd.rows(t) != before {
		t.Fatalf("a stale event changed state: %v -> %v", before, sd.rows(t))
	}
	if n := sd.count(t, "effects"); n != 1 {
		t.Fatalf("effects %d, want the repair's one effect", n)
	}
}

// Each failpoint aborts after some writes and before COMMIT. After a reopen
// none of the event, the attempt update or the effects may be visible.
func TestApplyTransactionCrashMatrix(t *testing.T) {
	for _, point := range []string{"after_event", "after_attempt", "after_effects"} {
		t.Run(point, func(t *testing.T) {
			s, sd, root := newSeededStore(t)
			before := sd.rows(t)
			crash := errors.New("crash at " + point)
			switch point {
			case "after_event":
				s.hooks.afterEvent = func() error { return crash }
			case "after_attempt":
				s.hooks.afterAttempt = func() error { return crash }
			case "after_effects":
				s.hooks.afterEffects = func() error { return crash }
			}
			ev := sd.event(1, workflow.EventAttemptCreated, 0)
			if _, err := s.ApplyWith(ctx, ev, owner); !errors.Is(err, crash) {
				t.Fatalf("err = %v", err)
			}
			if sd.rows(t) != before {
				t.Fatalf("partial writes survived on the live store: %v -> %v", before, sd.rows(t))
			}
			s.Close()

			reopened := openStore(t, root)
			reopened.now = fixedClock()
			sd.s = reopened
			if sd.rows(t) != before {
				t.Fatalf("partial writes survived a reopen: %v -> %v", before, sd.rows(t))
			}
			// The same event, retried, applies completely and gets the same effect id.
			r := mustApply(t, reopened, ev, owner)
			if r.Duplicate || sd.rows(t) != [4]int{1, 1, 2, 1} {
				t.Fatalf("retry: %+v rows %v", r, sd.rows(t))
			}
			var id string
			reopened.db.QueryRow(`SELECT id FROM effects`).Scan(&id)
			if id != effectID(ev.ID, 0) {
				t.Fatalf("effect id %s is not stable across the retry", id)
			}
		})
	}
}

func TestApplyConcurrentVersionCAS(t *testing.T) {
	root := newRoot(t)
	a := openStore(t, root)
	a.now = fixedClock()
	sd := seedProjectRunTaskAttempt(t, a, 1)
	b := openStore(t, root)
	b.now = fixedClock()

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			<-start
			// Different event ids, both issued at version 0.
			_, err := s.ApplyWith(ctx, sd.event(i+1, workflow.EventAttemptCreated, 0), owner)
			results <- err
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(results)
	var ok, conflicts int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, workflow.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("%d succeeded and %d conflicted, want 1 and 1", ok, conflicts)
	}
	if got := sd.rows(t); got != [4]int{1, 1, 2, 1} {
		t.Fatalf("rows %v: the loser left something behind", got)
	}
}

func TestApplyRefusesWrongRunStaleVersionAndBadInput(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	other := seedProjectRunTaskAttempt(t, s, 2)
	before := sd.rows(t)

	wrongRun := sd.event(1, workflow.EventAttemptCreated, 0)
	wrongRun.RunID = other.RunID
	if _, err := s.ApplyWith(ctx, wrongRun, owner); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("wrong run: %v", err)
	}
	missing := sd.event(2, workflow.EventAttemptCreated, 0)
	missing.AttemptID = hexID(77)
	if _, err := s.ApplyWith(ctx, missing, owner); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("unknown attempt: %v", err)
	}
	if _, err := s.ApplyWith(ctx, sd.event(3, workflow.EventAttemptCreated, 4), owner); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("stale expected version: %v", err)
	}
	for name, ev := range map[string]workflow.Event{
		"unknown kind": sd.event(4, "made_up", 0),
		"bad id":       func() workflow.Event { e := sd.event(5, workflow.EventAttemptCreated, 0); e.ID = "../../x"; return e }(),
		"bad run id":   func() workflow.Event { e := sd.event(6, workflow.EventAttemptCreated, 0); e.RunID = "R1"; return e }(),
		"bad payload": func() workflow.Event {
			e := sd.event(7, workflow.EventAttemptCreated, 0)
			e.PayloadHash = "xyz"
			return e
		}(),
		"negative": sd.event(8, workflow.EventAttemptCreated, -1),
	} {
		if _, err := s.ApplyWith(ctx, ev, owner); !errors.Is(err, workflow.ErrInvalidTransition) {
			t.Errorf("%s: err = %v, want ErrInvalidTransition", name, err)
		}
	}
	missingPayload := sd.event(9, workflow.EventAttemptCreated, 0)
	missingPayload.PayloadHash = hexID(1) + hexID(2) // well formed, never stored
	if _, err := s.ApplyWith(ctx, missingPayload, owner); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Errorf("payload that is not stored: %v", err)
	}
	if sd.rows(t) != before {
		t.Fatalf("refused events changed state: %v -> %v", before, sd.rows(t))
	}
	if other.count(t, "events") != 0 {
		t.Fatal("the other run was touched")
	}
}

func TestApplyNeedsControllerFacts(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	// Apply with no controller facts cannot create an attempt: nobody owns the writer.
	if _, err := s.Apply(ctx, sd.event(1, workflow.EventAttemptCreated, 0)); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
	mustApply(t, s, sd.event(1, workflow.EventAttemptCreated, 0), owner)
	mustApply(t, s, sd.event(2, workflow.EventBuilderStarted, 1), workflow.Observed{})
	sd.submitReady(t)
	v := sd.version(t)
	if _, err := s.ApplyWith(ctx, sd.event(3, workflow.EventBuilderSubmitted, v), workflow.Observed{SchedulingAllowed: true}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a submit with no settled session: %v", err)
	}
	r := mustApply(t, s, sd.event(3, workflow.EventBuilderSubmitted, v), workflow.Observed{SessionSettled: true, SchedulingAllowed: true})
	if r.Version != v+1 || sd.count(t, "effects") != 2 {
		t.Fatalf("receipt %+v, effects %d", r, sd.count(t, "effects"))
	}
}

func TestApplyPausesTheRunWithItsCode(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	mustApply(t, s, sd.event(1, workflow.EventBuilderStuck, 0), workflow.Observed{})
	var state, code string
	s.db.QueryRow(`SELECT state, pause_code FROM runs WHERE id=?`, sd.RunID).Scan(&state, &code)
	if state != "paused" || code != workflow.PauseStuck {
		t.Fatalf("run %s/%s", state, code)
	}
}

func TestApplyAcceptUsesPersistedEvidence(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	end, patch := sd.submitReady(t)
	sd.setAttempt(t, `implementation_state='submitted', check_state='passed', review_state='passed'`)
	v := sd.version(t)

	// No certificate and no review result: nothing persisted proves the claim.
	if _, err := s.ApplyWith(ctx, sd.event(1, workflow.EventAcceptRequested, v), owner); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("acceptance without evidence: %v", err)
	}

	checkJob, reviewJob := sd.job(t, "check", 1), sd.job(t, "reviewer", 2)
	in, cmd, env, log := sd.artifact(t, "inputs"), sd.artifact(t, "command"), sd.artifact(t, "env"), sd.artifact(t, "log")
	sd.exec(t, `INSERT INTO check_certificates(id,attempt_id,job_id,before_inputs_hash,after_inputs_hash,command_hash,environment_hash,exit_code,log_hash,complete,created_ms)
		VALUES(?,?,?,?,?,?,?,0,?,1,1)`, hexID(2001), sd.AttemptID, checkJob, in, in, cmd, env, log)
	// A review result bound to a different tree is not evidence for this one.
	result := sd.artifact(t, "review result")
	elsewhere := sd.artifact(t, "another tree")
	sd.exec(t, `INSERT INTO review_results(job_id,attempt_id,result_hash,patch_hash,tree_hash,status,created_ms) VALUES(?,?,?,?,?,'passed',1)`,
		reviewJob, sd.AttemptID, result, patch, elsewhere)
	if _, err := s.ApplyWith(ctx, sd.event(2, workflow.EventAcceptRequested, v), owner); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("acceptance on a review of another tree: %v", err)
	}
	sd.exec(t, `UPDATE review_results SET tree_hash=? WHERE job_id=?`, end, reviewJob)

	r := mustApply(t, s, sd.event(3, workflow.EventAcceptRequested, v), owner)
	var accepted *string
	var ms *int64
	s.db.QueryRow(`SELECT accepted_attempt_id FROM tasks WHERE run_id=? AND id=?`, sd.RunID, sd.TaskID).Scan(&accepted)
	s.db.QueryRow(`SELECT accepted_ms FROM attempts WHERE id=?`, sd.AttemptID).Scan(&ms)
	if r.Version != v+1 || accepted == nil || *accepted != sd.AttemptID || ms == nil || *ms != 5000 {
		t.Fatalf("receipt %+v accepted %v ms %v", r, accepted, ms)
	}
	// A second acceptance of the same task cannot replace the first.
	if _, err := s.ApplyWith(ctx, sd.event(4, workflow.EventAcceptRequested, v+1), owner); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("second acceptance: %v", err)
	}
}

func TestApplyAcceptIgnoresBudget(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	sd.submitReady(t)
	sd.setAttempt(t, `implementation_state='submitted', check_state='not_applicable', review_state='not_applicable', budget_state='exhausted'`)
	v := sd.version(t)
	obs := workflow.Observed{NoCheckPolicy: true, NoReviewPolicy: true, Budget: workflow.BudgetExhausted}
	if r := mustApply(t, s, sd.event(1, workflow.EventAcceptRequested, v), obs); r.Version != v+1 {
		t.Fatalf("receipt %+v", r)
	}
}

func TestApplyRunLevelStop(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	ev := sd.event(1, workflow.EventStopRequested, 0)
	ev.AttemptID = ""
	r := mustApply(t, s, ev, workflow.Observed{})
	var state, code string
	var rv int64
	s.db.QueryRow(`SELECT state, pause_code, version FROM runs WHERE id=?`, sd.RunID).Scan(&state, &code, &rv)
	if r.Version != 1 || state != "stopping" || code != workflow.PauseStopping || rv != 1 {
		t.Fatalf("receipt %+v run %s/%s v%d", r, state, code, rv)
	}
	var attemptID *string
	s.db.QueryRow(`SELECT attempt_id FROM effects WHERE source_event_id=?`, ev.ID).Scan(&attemptID)
	if attemptID != nil || sd.version(t) != 0 {
		t.Fatalf("a run-level stop touched the attempt: effect attempt %v, attempt version %d", attemptID, sd.version(t))
	}
	// A second run-level event issued at the old run version loses.
	again := sd.event(2, workflow.EventStopRequested, 0)
	again.AttemptID = ""
	if _, err := s.ApplyWith(ctx, again, workflow.Observed{}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("stale run version: %v", err)
	}
}

func TestApplyRefusesEventsForFinishedRuns(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	sd.exec(t, `UPDATE runs SET state='finished' WHERE id=?`, sd.RunID)
	if _, err := s.ApplyWith(ctx, sd.event(1, workflow.EventAttemptCreated, 0), owner); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
}

func TestEffectIdsAreStablePerEventAndOrdinal(t *testing.T) {
	a, b := effectID(hexID(1), 0), effectID(hexID(1), 0)
	if a != b || len(a) != 32 || a == effectID(hexID(1), 1) || a == effectID(hexID(2), 0) {
		t.Fatalf("effect ids %s %s", a, b)
	}
	if effectNonce(hexID(1), 0) == a {
		t.Fatal("an effect's nonce must not equal its id")
	}
}

func TestApplyWaiverCountsOnlyForTheTreeItWasRecordedFor(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	end, _ := sd.submitReady(t)
	sd.setAttempt(t, `implementation_state='submitted', check_state='not_applicable', review_state='changes_requested'`)
	v := sd.version(t)
	reason := sd.artifact(t, "the planner read the diff")
	elsewhere := sd.artifact(t, "a tree from before the last edit")
	sd.exec(t, `INSERT INTO waivers(id,attempt_id,scope,reason_hash,actor,evidence_tree_hash,created_ms) VALUES(?,?,'review',?,'planner',?,1)`,
		hexID(3001), sd.AttemptID, reason, elsewhere)
	if _, err := s.ApplyWith(ctx, sd.event(1, workflow.EventWaiveRequested, v), workflow.Observed{NoCheckPolicy: true}); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("a waiver for another tree was honoured: %v", err)
	}
	sd.exec(t, `UPDATE waivers SET evidence_tree_hash=? WHERE id=?`, end, hexID(3001))
	r := mustApply(t, s, sd.event(2, workflow.EventWaiveRequested, v), workflow.Observed{NoCheckPolicy: true})
	var review string
	s.db.QueryRow(`SELECT review_state FROM attempts WHERE id=?`, sd.AttemptID).Scan(&review)
	if review != "waived" || r.Version != v+1 {
		t.Fatalf("review %s receipt %+v", review, r)
	}
	// Once the tree changes, the same waiver no longer covers the attempt.
	sd.setAttempt(t, `end_snapshot_hash=?`, elsewhere)
	if _, err := s.ApplyWith(ctx, sd.event(3, workflow.EventAcceptRequested, r.Version), workflow.Observed{NoCheckPolicy: true}); !errors.Is(err, workflow.ErrIncompleteEvidence) {
		t.Fatalf("acceptance after the tree moved on: %v", err)
	}
}
