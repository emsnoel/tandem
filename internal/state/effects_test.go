package state

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// clock is a settable time source shared by a store and its test.
type clock struct{ ms int64 }

func (c *clock) now() time.Time          { return time.UnixMilli(c.ms) }
func (c *clock) advance(d time.Duration) { c.ms += d.Milliseconds() }

func effectStore(t *testing.T) (*Store, *seed, *clock) {
	t.Helper()
	s, sd, _ := newSeededStore(t)
	c := &clock{ms: 1_000_000}
	s.WithClock(c.now)
	return s, sd, c
}

// pendingEffect applies attempt_created, which leaves one pending
// builder_dispatch effect, and returns its id.
func pendingEffect(t *testing.T, s *Store, sd *seed, n int) string {
	t.Helper()
	ev := sd.event(n, workflow.EventAttemptCreated, 0)
	mustApply(t, s, ev, owner)
	return effectID(ev.ID, 0)
}

// rawEffect adds an event and an effect directly, for states Apply does not produce.
func rawEffect(t *testing.T, sd *seed, id, eventID, kind, state string, availableMS int64) {
	t.Helper()
	sd.exec(t, `INSERT INTO events(id,run_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?,'stop_requested',?,0,1,1)`,
		eventID, sd.RunID, sd.PayloadHash)
	sd.exec(t, `INSERT INTO effects(id,run_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms)
		VALUES(?,?,?,?,0,?,?,?,0,?,?)`, id, sd.RunID, eventID, kind, sd.PayloadHash, derive(id, "nonce", 0), state, availableMS, availableMS)
}

func stateOf(t *testing.T, s *Store, id string) (state string, fence int64, retries int) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT state,fence,retry_count FROM effects WHERE id=?`, id).Scan(&state, &fence, &retries); err != nil {
		t.Fatal(err)
	}
	return
}

func TestClaimEffectLeasesOneEffectAndAdvancesTheFence(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)

	eff, err := s.ClaimEffect(ctx, "worker-a", sd.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if eff.ID != id || eff.State != "delivering" || eff.Fence != 1 || eff.Kind != workflow.EffectBuilderDispatch || eff.Nonce != effectNonce(sd.event(1, "", 0).ID, 0) {
		t.Fatalf("claimed %+v", eff)
	}
	var owner string
	var until int64
	s.db.QueryRow(`SELECT lease_owner,lease_until_ms FROM effects WHERE id=?`, id).Scan(&owner, &until)
	if owner != "worker-a" || until != c.ms+30_000 {
		t.Fatalf("lease %s until %d, want 30 s from now", owner, until)
	}
	if _, err := s.ClaimEffect(ctx, "worker-b", sd.RunID); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("a leased effect was claimed again: %v", err)
	}
	if _, err := s.ClaimEffect(ctx, "", sd.RunID); err == nil {
		t.Fatal("a claim with no owner")
	}
}

func TestClaimRespectsAvailabilityOrderAndOneAtATime(t *testing.T) {
	s, sd, c := effectStore(t)
	rawEffect(t, sd, hexID(1), hexID(11), "cancel", "pending", c.ms+5_000) // not yet
	rawEffect(t, sd, hexID(2), hexID(12), "cancel", "pending", c.ms-2)
	rawEffect(t, sd, hexID(3), hexID(13), "cancel", "pending", c.ms-3)

	first, err := s.ClaimEffect(ctx, "w", sd.RunID)
	if err != nil || first.ID != hexID(3) {
		t.Fatalf("first claim %+v %v: the oldest available effect goes first", first, err)
	}
	second, _ := s.ClaimEffect(ctx, "w", sd.RunID)
	if second.ID != hexID(2) {
		t.Fatalf("second claim %+v", second)
	}
	if _, err := s.ClaimEffect(ctx, "w", sd.RunID); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("an effect that is not available yet was claimed: %v", err)
	}
	c.advance(6 * time.Second)
	if got, err := s.ClaimEffect(ctx, "w", sd.RunID); err != nil || got.ID != hexID(1) {
		t.Fatalf("after its availability time: %+v %v", got, err)
	}
}

func TestTwoStoresNeverClaimTheSameEffect(t *testing.T) {
	a, sd, c := effectStore(t)
	b := openStore(t, filepathDir(a))
	b.WithClock(c.now)
	for i := 0; i < 6; i++ {
		rawEffect(t, sd, hexID(20+i), hexID(40+i), "cancel", "pending", c.ms-10)
	}
	var wg sync.WaitGroup
	got := make(chan string, 12)
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			for {
				eff, err := s.ClaimEffect(ctx, "w", sd.RunID)
				if errors.Is(err, ErrNoEffect) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				got <- eff.ID
			}
		}(s)
	}
	wg.Wait()
	close(got)
	seen := map[string]bool{}
	for id := range got {
		if seen[id] {
			t.Fatalf("effect %s was claimed twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 6 {
		t.Fatalf("%d of 6 effects claimed", len(seen))
	}
}

func TestExpiredFenceCannotRecordReceipt(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	old, _ := s.ClaimEffect(ctx, "old-worker", sd.RunID)
	receipt := sd.artifact(t, "receipt from the paused worker")

	c.advance(31 * time.Second) // the old worker is paused past its lease
	next, err := s.ClaimEffect(ctx, "new-worker", sd.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != id || next.Fence != old.Fence+1 {
		t.Fatalf("second owner %+v, old %+v", next, old)
	}
	if next.State != "delivery_unknown" {
		t.Fatalf("an expired lease means the send may have happened: state %s", next.State)
	}

	if err := s.RecordDelivery(ctx, id, old.Fence, receipt); !errors.Is(err, ErrStaleFence) || !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("the old worker's receipt: %v", err)
	}
	state, fence, _ := stateOf(t, s, id)
	var stored *string
	s.db.QueryRow(`SELECT receipt_hash FROM effects WHERE id=?`, id).Scan(&stored)
	if state != "delivery_unknown" || fence != next.Fence || stored != nil {
		t.Fatalf("the refused write changed the effect: %s fence %d receipt %v", state, fence, stored)
	}
	// Every write after a claim carries the fence, and none of the old one lands.
	if err := s.RecordNotSent(ctx, id, old.Fence, ""); !errors.Is(err, ErrStaleFence) {
		t.Errorf("RecordNotSent: %v", err)
	}
	if err := s.MarkDeliveryUnknown(ctx, id, old.Fence, ""); !errors.Is(err, ErrStaleFence) {
		t.Errorf("MarkDeliveryUnknown: %v", err)
	}
	if err := s.HeartbeatEffect(ctx, id, old.Fence); !errors.Is(err, ErrStaleFence) {
		t.Errorf("HeartbeatEffect: %v", err)
	}
	if err := s.ReleaseReconcile(ctx, id, old.Fence); !errors.Is(err, ErrStaleFence) {
		t.Errorf("ReleaseReconcile: %v", err)
	}
}

func TestRecordDeliveryKeepsTheEffectUnconfirmed(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	eff, _ := s.ClaimEffect(ctx, "w", sd.RunID)
	receipt := sd.artifact(t, "transport accepted the prompt")
	if err := s.RecordDelivery(ctx, id, eff.Fence, receipt); err != nil {
		t.Fatal(err)
	}
	state, _, _ := stateOf(t, s, id)
	var until int64
	s.db.QueryRow(`SELECT lease_until_ms FROM effects WHERE id=?`, id).Scan(&until)
	if state != "delivering" || until != c.ms+int64(AckWindow/time.Millisecond) {
		t.Fatalf("state %s lease until %d: an accepted send waits for the nonce, not for the 30 s lease", state, until)
	}
	// Wrong nonce does not acknowledge; the matching one does, once or repeatedly.
	if err := s.AcknowledgeEffect(ctx, id, "not-the-nonce"); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("wrong nonce: %v", err)
	}
	if st, _, _ := stateOf(t, s, id); st != "delivering" {
		t.Fatalf("a wrong nonce moved the effect to %s", st)
	}
	nonce := effectNonce(sd.event(1, "", 0).ID, 0)
	for i := 0; i < 2; i++ {
		if err := s.AcknowledgeEffect(ctx, id, nonce); err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
	}
	if st, _, _ := stateOf(t, s, id); st != "acknowledged" {
		t.Fatalf("state %s", st)
	}
}

func TestNoAcknowledgementWithinTheWindowBecomesUnknown(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	eff, _ := s.ClaimEffect(ctx, "w", sd.RunID)
	s.RecordDelivery(ctx, id, eff.Fence, sd.artifact(t, "accepted"))
	c.advance(AckWindow + time.Second)
	got, err := s.ClaimEffect(ctx, "w2", sd.RunID)
	if err != nil || got.ID != id || got.State != "delivery_unknown" {
		t.Fatalf("%+v %v", got, err)
	}
	// A late nonce-bound acknowledgement still resolves it.
	if err := s.AcknowledgeEffect(ctx, id, got.Nonce); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := stateOf(t, s, id); st != "acknowledged" {
		t.Fatalf("state %s", st)
	}
}

func TestDefiniteNotSentRetriesWithBackoffThenFails(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	wantDelay := []time.Duration{time.Second, 2 * time.Second}
	for i := 0; i < 3; i++ {
		eff, err := s.ClaimEffect(ctx, "w", sd.RunID)
		if err != nil || eff.ID != id {
			t.Fatalf("attempt %d: %+v %v", i+1, eff, err)
		}
		if err := s.RecordNotSent(ctx, id, eff.Fence, ""); err != nil {
			t.Fatal(err)
		}
		st, fence, retries := stateOf(t, s, id)
		if i < 2 {
			var avail int64
			s.db.QueryRow(`SELECT available_ms FROM effects WHERE id=?`, id).Scan(&avail)
			if st != "pending" || retries != i+1 || avail != c.ms+wantDelay[i].Milliseconds() || fence != eff.Fence {
				t.Fatalf("after failure %d: %s retries %d available +%dms", i+1, st, retries, avail-c.ms)
			}
			if _, err := s.ClaimEffect(ctx, "w", sd.RunID); !errors.Is(err, ErrNoEffect) {
				t.Fatalf("retried before its backoff: %v", err)
			}
			c.advance(wantDelay[i])
		} else if st != "failed" || retries != 3 {
			t.Fatalf("after the third failure: %s retries %d", st, retries)
		}
	}
	if _, err := s.ClaimEffect(ctx, "w", sd.RunID); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("a failed effect was claimed: %v", err)
	}
}

func TestAmbiguousSendIsNeverReclaimedForSending(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	eff, _ := s.ClaimEffect(ctx, "w", sd.RunID)
	evidence := sd.artifact(t, "transport error: connection reset after write")
	if err := s.MarkDeliveryUnknown(ctx, id, eff.Fence, evidence); err != nil {
		t.Fatal(err)
	}
	var got *string
	s.db.QueryRow(`SELECT receipt_hash FROM effects WHERE id=?`, id).Scan(&got)
	if got == nil || *got != evidence {
		t.Fatalf("the evidence was not retained: %v", got)
	}
	// Reclaiming it is for reconciliation only: the state says so.
	rec, err := s.ClaimEffect(ctx, "w", sd.RunID)
	if err != nil || rec.State != "delivery_unknown" || rec.Fence != eff.Fence+1 {
		t.Fatalf("%+v %v", rec, err)
	}
	if err := s.ReleaseReconcile(ctx, id, rec.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimEffect(ctx, "w", sd.RunID); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("reconciliation is retried at once instead of after its delay: %v", err)
	}
	c.advance(31 * time.Second)
	if again, err := s.ClaimEffect(ctx, "w", sd.RunID); err != nil || again.State != "delivery_unknown" {
		t.Fatalf("%+v %v", again, err)
	}
	if st, _, _ := stateOf(t, s, id); st == "pending" || st == "delivering" {
		t.Fatalf("an ambiguous effect became %s", st)
	}
}

func TestAnUnknownWriterBlocksNewBuilderDispatchesInTheRun(t *testing.T) {
	s, sd, c := effectStore(t)
	rawEffect(t, sd, hexID(1), hexID(11), workflow.EffectBuilderDispatch, "delivery_unknown", c.ms+3_600_000) // not reconcilable yet
	rawEffect(t, sd, hexID(2), hexID(12), workflow.EffectBuilderDispatch, "pending", c.ms-1)
	rawEffect(t, sd, hexID(3), hexID(13), workflow.EffectCancel, "pending", c.ms-1)
	got, err := s.ClaimEffect(ctx, "w", sd.RunID)
	if err != nil || got.ID != hexID(3) {
		t.Fatalf("claimed %+v %v: only the cancel may run while the writer's state is unknown", got, err)
	}
	if _, err := s.ClaimEffect(ctx, "w", sd.RunID); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("a second builder dispatch was claimed beside an unknown one: %v", err)
	}
	// Another run is unaffected.
	other := seedProjectRunTaskAttempt(t, s, 2)
	rawEffect(t, other, hexID(4), hexID(14), workflow.EffectBuilderDispatch, "pending", c.ms-1)
	if got, err := s.ClaimEffect(ctx, "w", other.RunID); err != nil || got.ID != hexID(4) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestHeartbeatExtendsOnlyTheCurrentLease(t *testing.T) {
	s, sd, c := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	eff, _ := s.ClaimEffect(ctx, "w", sd.RunID)
	c.advance(10 * time.Second)
	if err := s.HeartbeatEffect(ctx, id, eff.Fence); err != nil {
		t.Fatal(err)
	}
	var until int64
	s.db.QueryRow(`SELECT lease_until_ms FROM effects WHERE id=?`, id).Scan(&until)
	if until != c.ms+30_000 {
		t.Fatalf("lease until %d, want %d", until, c.ms+30_000)
	}
	c.advance(25 * time.Second) // 35 s after the claim: only the heartbeat keeps it live
	if _, err := s.ClaimEffect(ctx, "x", sd.RunID); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("a heartbeated lease expired: %v", err)
	}
	s.RecordNotSent(ctx, id, eff.Fence, "")
	if err := s.HeartbeatEffect(ctx, id, eff.Fence); err == nil {
		t.Fatal("heartbeat on an effect that is no longer delivering")
	}
}

func TestEffectOperationsRefuseBadInput(t *testing.T) {
	s, sd, _ := effectStore(t)
	id := pendingEffect(t, s, sd, 1)
	if err := s.RecordDelivery(ctx, id, 0, sd.artifact(t, "r")); err == nil {
		t.Error("a delivery for an effect nobody claimed")
	}
	eff, _ := s.ClaimEffect(ctx, "w", sd.RunID)
	if err := s.RecordDelivery(ctx, id, eff.Fence, "bad"); err == nil {
		t.Error("a malformed receipt hash")
	}
	if err := s.RecordDelivery(ctx, id, eff.Fence, hexID(1)+hexID(2)); err == nil {
		t.Error("a receipt that is not an artifact")
	}
	if err := s.RecordDelivery(ctx, hexID(9999), 1, sd.artifact(t, "r")); err == nil {
		t.Error("a delivery for an effect that does not exist")
	}
	if got, err := s.GetEffect(ctx, id); err != nil || got.State != "delivering" || got.Fence != eff.Fence {
		t.Fatalf("%+v %v", got, err)
	}
}

// A claim takes only the ticked run's effects. Runs in one checkout share the
// store, and a stopped run is paused, so no run-state filter would exclude its
// effects.
func TestClaimTakesOnlyTheTickedRunsEffects(t *testing.T) {
	for _, runState := range []string{"running", "paused", "stopping", "finished", "archived"} {
		for _, effState := range []string{"pending", "delivery_unknown", "delivering"} {
			t.Run(runState+"/"+effState, func(t *testing.T) {
				s, sd, c := effectStore(t)
				other := seedProjectRunTaskAttempt(t, s, 2)
				other.exec(t, `UPDATE runs SET state=?, pause_code=CASE WHEN ?='paused' THEN 'stopped' END WHERE id=?`, runState, runState, other.RunID)
				theirs := hexID(911)
				rawEffect(t, other, theirs, hexID(912), workflow.EffectReviewDispatch, effState, c.ms-10)
				other.exec(t, `UPDATE effects SET lease_until_ms=? WHERE id=?`, c.ms-1, theirs)

				if eff, err := s.ClaimEffect(ctx, "worker-a", sd.RunID); !errors.Is(err, ErrNoEffect) {
					t.Fatalf("run A claimed run B's effect: %+v %v", eff, err)
				}
				ours := hexID(913)
				rawEffect(t, sd, ours, hexID(914), workflow.EffectReviewDispatch, "pending", c.ms-1)
				if eff, err := s.ClaimEffect(ctx, "worker-a", sd.RunID); err != nil || eff.ID != ours {
					t.Fatalf("run A's own effect was not claimed first: %+v %v", eff, err)
				}
				if eff, err := s.ClaimEffect(ctx, "worker-b", other.RunID); runState != "archived" && runState != "finished" && (err != nil || eff.ID != theirs) {
					t.Errorf("run B's own claim did not take its effect: %+v %v", eff, err)
				}
			})
		}
	}
}

// Archiving a run cancels what it never started: an archived run is never
// driven again, so a pending effect of it can only be claimed by mistake.
func TestArchiveCancelsTheRunsPendingEffects(t *testing.T) {
	s, sd, c := effectStore(t)
	id := hexID(903)
	rawEffect(t, sd, id, hexID(904), workflow.EffectCheckDispatch, "pending", c.ms-1)
	sd.exec(t, `UPDATE runs SET state='paused', pause_code='stopped' WHERE id=?`, sd.RunID)
	if err := s.ArchiveRun(ctx, sd.RunID); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := stateOf(t, s, id); st != "cancelled" {
		t.Errorf("state %s after archiving, want cancelled", st)
	}
}
