package state

import (
	"errors"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// settleEnv is a paused run with no writer and one builder prompt whose
// delivery is open, with the builder job waiting on it.
type settleEnv struct {
	s       *Store
	sd      *seed
	effect  string
	job     string
	payload string
}

func newSettleEnv(t *testing.T) *settleEnv {
	t.Helper()
	s, sd, _ := effectStore(t)
	e := &settleEnv{s: s, sd: sd, effect: hexID(2001), job: hexID(2002), payload: sd.artifact(t, `{"effect_id":"x","actor":"owner","reason":"gave up"}`)}
	sd.exec(t, `UPDATE runs SET state='paused', pause_code='stopped'`)
	rawEffect(t, sd, e.effect, hexID(2003), workflow.EffectBuilderDispatch, "delivery_unknown", 1)
	sd.exec(t, `UPDATE effects SET attempt_id=? WHERE id=?`, sd.AttemptID, e.effect)
	sd.exec(t, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'builder','running',?,?,'test')`,
		e.job, sd.AttemptID, sd.PacketHash, sd.PacketHash)
	return e
}

func (e *settleEnv) settle() error {
	return e.s.AbandonDelivery(ctx, e.sd.RunID, e.effect, e.payload)
}

func TestAbandonDelivery(t *testing.T) {
	for _, st := range []string{"delivering", "delivery_unknown"} {
		t.Run("settles a prompt that is "+st, func(t *testing.T) {
			e := newSettleEnv(t)
			e.sd.exec(t, `UPDATE effects SET state=? WHERE id=?`, st, e.effect)
			before, err := e.s.GetRun(ctx, e.sd.RunID)
			if err != nil {
				t.Fatal(err)
			}
			versionBefore, attemptBefore := before.Version, e.sd.version(t)
			if err := e.settle(); err != nil {
				t.Fatal(err)
			}
			if got, _, _ := stateOf(t, e.s, e.effect); got != "cancelled" {
				t.Fatalf("the effect is %s", got)
			}
			var job string
			if err := e.s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, e.job).Scan(&job); err != nil || job != "cancelled" {
				t.Fatalf("the job is %s (%v)", job, err)
			}
			events, err := e.s.RunEvents(ctx, e.sd.RunID)
			if err != nil || len(events) != 2 {
				t.Fatalf("events %+v (%v)", events, err)
			}
			last := events[len(events)-1]
			if last.Kind != EventDeliveryAbandoned || last.PayloadHash != e.payload || last.AttemptID != e.sd.AttemptID {
				t.Fatalf("the event is %+v", last)
			}
			// It records a statement and moves nothing the workflow versions.
			run, err := e.s.GetRun(ctx, e.sd.RunID)
			if err != nil || run.Version != versionBefore || run.State != "paused" {
				t.Fatalf("run %+v (%v), version was %d", run, err, versionBefore)
			}
			if got := e.sd.version(t); got != attemptBefore {
				t.Fatalf("attempt version %d, was %d", got, attemptBefore)
			}
			if n, err := e.s.ManualInterventions(ctx, e.sd.RunID); err != nil || n != 0 {
				t.Fatalf("%d interventions (%v)", n, err)
			}
			if err := e.s.ArchiveRun(ctx, e.sd.RunID); err != nil {
				t.Fatalf("the settled run does not archive: %v", err)
			}
		})
	}

	for _, st := range []string{"running", "finished", "archived"} {
		t.Run("refuses a run that is "+st, func(t *testing.T) {
			e := newSettleEnv(t)
			e.sd.exec(t, `UPDATE runs SET state=?`, st)
			e.refused(t, workflow.ErrConflict)
		})
	}

	t.Run("accepts a stopping run with no writer", func(t *testing.T) {
		e := newSettleEnv(t)
		e.sd.exec(t, `UPDATE runs SET state='stopping', pause_code=NULL`)
		if err := e.settle(); err != nil {
			t.Fatal(err)
		}
	})

	for _, st := range []string{"pending", "acknowledged", "failed", "cancelled"} {
		t.Run("refuses an effect that is "+st, func(t *testing.T) {
			e := newSettleEnv(t)
			e.sd.exec(t, `UPDATE effects SET state=? WHERE id=?`, st, e.effect)
			e.refused(t, workflow.ErrConflict)
		})
	}

	t.Run("refuses an effect from another run", func(t *testing.T) {
		e := newSettleEnv(t)
		other := seedProjectRunTaskAttempt(t, e.s, 7)
		other.exec(t, `UPDATE runs SET state='paused' WHERE id=?`, other.RunID)
		if err := e.s.AbandonDelivery(ctx, other.RunID, e.effect, e.payload); !errors.Is(err, workflow.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
		e.unchanged(t)
	})

	t.Run("refuses an effect that does not exist", func(t *testing.T) {
		e := newSettleEnv(t)
		e.effect = hexID(2999)
		if err := e.settle(); !errors.Is(err, workflow.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("refuses while the writer lease is held", func(t *testing.T) {
		e := newSettleEnv(t)
		if _, err := e.s.AcquireLease(ctx, e.sd.CheckoutID, "someone", 1); err != nil {
			t.Fatal(err)
		}
		e.refused(t, workflow.ErrBusy)
	})

	t.Run("needs a stored statement", func(t *testing.T) {
		e := newSettleEnv(t)
		if err := e.s.AbandonDelivery(ctx, e.sd.RunID, e.effect, ""); err == nil {
			t.Fatal("no payload")
		}
		if err := e.s.AbandonDelivery(ctx, e.sd.RunID, e.effect, strings.Repeat("a", 64)); err == nil {
			t.Fatal("a payload that is not stored")
		}
		e.unchanged(t)
	})
}

func (e *settleEnv) refused(t *testing.T, want error) {
	t.Helper()
	before, _, _ := stateOf(t, e.s, e.effect)
	if err := e.settle(); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if got, _, _ := stateOf(t, e.s, e.effect); got != before {
		t.Fatalf("a refusal moved the effect from %s to %s", before, got)
	}
	e.noIntervention(t)
}

func (e *settleEnv) unchanged(t *testing.T) {
	t.Helper()
	if got, _, _ := stateOf(t, e.s, e.effect); got != "delivery_unknown" {
		t.Fatalf("the effect is %s", got)
	}
	e.noIntervention(t)
}

func (e *settleEnv) noIntervention(t *testing.T) {
	t.Helper()
	var n int
	if err := e.s.db.QueryRow(`SELECT count(*) FROM events WHERE kind=?`, EventDeliveryAbandoned).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d events after a refusal (%v)", n, err)
	}
}

// Each kind of prompt ends the job that waits on it, and no other.
func TestSettleDeliveryEndsOnlyItsOwnJob(t *testing.T) {
	e := newSettleEnv(t)
	review := hexID(2004)
	e.sd.exec(t, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'reviewer','running',?,?,'test')`,
		review, e.sd.AttemptID, e.sd.PacketHash, e.sd.PacketHash)
	if err := e.settle(); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := e.s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, review).Scan(&got); err != nil || got != "running" {
		t.Fatalf("settling a builder prompt left the reviewer job %s (%v)", got, err)
	}

	e = newSettleEnv(t)
	e.sd.exec(t, `UPDATE effects SET kind='review_prompt' WHERE id=?`, e.effect)
	e.sd.exec(t, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'reviewer','running',?,?,'test')`,
		review, e.sd.AttemptID, e.sd.PacketHash, e.sd.PacketHash)
	e.sd.exec(t, `UPDATE effects SET job_id=? WHERE id=?`, review, e.effect)
	if err := e.settle(); err != nil {
		t.Fatal(err)
	}
	if err := e.s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, review).Scan(&got); err != nil || got != "cancelled" {
		t.Fatalf("the reviewer job is %s (%v)", got, err)
	}
	if err := e.s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, e.job).Scan(&got); err != nil || got != "running" {
		t.Fatalf("settling a review prompt left the builder job %s (%v)", got, err)
	}
}

// A builder or a replay can never issue this event: it is the store's alone.
func TestDeliveryAbandonedIsNotAWorkflowEvent(t *testing.T) {
	e := newSettleEnv(t)
	_, err := e.s.ApplyWith(ctx, workflow.Event{ID: hexID(2100), RunID: e.sd.RunID, AttemptID: e.sd.AttemptID, Kind: EventDeliveryAbandoned,
		PayloadHash: e.payload, ExpectedVersion: e.sd.version(t)}, workflow.Observed{})
	if err == nil {
		t.Fatal("the event applied through the workflow")
	}
}

// A store holding the event opens again and still archives, and the payload is
// one of the run's artifacts, so an export carries it.
func TestStoreWithAnAbandonedDeliveryReopensAndArchives(t *testing.T) {
	e := newSettleEnv(t)
	if err := e.settle(); err != nil {
		t.Fatal(err)
	}
	root := filepathDir(e.s)
	run := e.sd.RunID
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, root)
	if v, err := s.SchemaVersion(); err != nil || v != LatestSchema() {
		t.Fatalf("schema %d (%v)", v, err)
	}
	uses, err := s.RunArtifacts(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range uses {
		found = found || u.Hash == e.payload
	}
	if !found {
		t.Fatalf("the statement is not among the run's artifacts: %+v", uses)
	}
	if err := s.ArchiveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
}
