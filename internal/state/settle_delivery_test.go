package state

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/emsnoel/tandem/internal/workflow"
)

// settleEnv is a paused run with no writer and one builder prompt whose
// delivery is open, with the builder job waiting on it.
type settleEnv struct {
	s      *Store
	sd     *seed
	effect string
	job    string
	reason string
}

func newSettleEnv(t *testing.T) *settleEnv {
	t.Helper()
	s, sd, _ := effectStore(t)
	e := &settleEnv{s: s, sd: sd, effect: hexID(2001), job: hexID(2002), reason: sd.artifact(t, "the pane never showed it")}
	sd.exec(t, `UPDATE runs SET state='paused', pause_code='stopped'`)
	rawEffect(t, sd, e.effect, hexID(2003), workflow.EffectBuilderDispatch, "delivery_unknown", 1)
	sd.exec(t, `UPDATE effects SET attempt_id=? WHERE id=?`, sd.AttemptID, e.effect)
	sd.exec(t, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'builder','running',?,?,'test')`,
		e.job, sd.AttemptID, sd.PacketHash, sd.PacketHash)
	return e
}

func (e *settleEnv) settle() error {
	return e.s.SettleDeliveryNotRead(ctx, e.sd.RunID, e.effect, e.reason, "owner")
}

func TestSettleDeliveryNotRead(t *testing.T) {
	for _, st := range []string{"delivering", "delivery_unknown"} {
		t.Run("settles a prompt that is "+st, func(t *testing.T) {
			e := newSettleEnv(t)
			e.sd.exec(t, `UPDATE effects SET state=? WHERE id=?`, st, e.effect)
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
			var effect, actor, reason, task string
			if err := e.s.db.QueryRow(`SELECT effect_id,actor,reason_hash,task_id FROM manual_interventions WHERE run_id=?`, e.sd.RunID).
				Scan(&effect, &actor, &reason, &task); err != nil {
				t.Fatal(err)
			}
			if effect != e.effect || actor != "owner" || reason != e.reason || task != e.sd.TaskID {
				t.Fatalf("the row says %s %s %s %s", effect, actor, reason, task)
			}
			if n, err := e.s.ManualInterventions(ctx, e.sd.RunID); err != nil || n != 1 {
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
		if err := e.s.SettleDeliveryNotRead(ctx, other.RunID, e.effect, e.reason, "owner"); !errors.Is(err, workflow.ErrConflict) {
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

	t.Run("needs a reason and who stated it", func(t *testing.T) {
		e := newSettleEnv(t)
		if err := e.s.SettleDeliveryNotRead(ctx, e.sd.RunID, e.effect, "", "owner"); err == nil {
			t.Fatal("no reason")
		}
		if err := e.s.SettleDeliveryNotRead(ctx, e.sd.RunID, e.effect, e.reason, ""); err == nil {
			t.Fatal("no actor")
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
	if n := e.sd.count(t, "manual_interventions"); n != 0 {
		t.Fatalf("%d interventions after a refusal", n)
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

// A settled delivery replaces no attempt, so it must not look like a reconcile:
// that attempt can still be reconciled, and no attempt reads as handed over.
func TestSettleDeliveryIsNotAReconcile(t *testing.T) {
	e := newSettleEnv(t)
	if err := e.settle(); err != nil {
		t.Fatal(err)
	}
	if from, ok, err := e.s.ReconcileInto(ctx, e.sd.AttemptID); err != nil || ok {
		t.Fatalf("the attempt reads as a reconcile of %s (%v)", from, err)
	}
	var n int
	if err := e.s.db.QueryRow(`SELECT count(*) FROM manual_interventions WHERE from_attempt_id=? OR to_attempt_id=?`, e.sd.AttemptID, e.sd.AttemptID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows name the attempt (%v)", n, err)
	}
}

// The refusal an owner reads names the command that ends it, for each open
// delivery of the run.
func TestArchiveRefusalNamesSettleDelivery(t *testing.T) {
	e := newSettleEnv(t)
	err := e.s.ArchiveRun(ctx, e.sd.RunID)
	if !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	want := "tandem state settle-delivery --run " + e.sd.RunID + " --effect " + e.effect + " --not-read --because WHY --root DIR"
	if !strings.Contains(err.Error(), "has 1 delivery whose outcome is open") || !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal is %q", err)
	}
}

// Schema 7 rebuilds manual_interventions, so a store's reconciles must come
// through it, and still be counted and found.
func TestSchema7KeepsEveryReconcile(t *testing.T) {
	root := newRoot(t)
	files := fstest.MapFS{}
	names, err := fs.Glob(embedded, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/007" {
			continue
		}
		body, err := fs.ReadFile(embedded, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: body}
	}
	old, err := open(context.Background(), root, files, testHooks{})
	if err != nil {
		t.Fatal(err)
	}
	sd := seedProjectRunTaskAttempt(t, old, 3)
	reason, next := sd.artifact(t, "why"), hexID(3001)
	sd.exec(t, `INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,
		start_snapshot_hash,packet_hash,version,is_current,created_ms) VALUES(?,?,?,2,'pending','pending','pending','available',?,?,0,0,1)`,
		next, sd.RunID, sd.TaskID, sd.StartHash, sd.PacketHash)
	sd.exec(t, `INSERT INTO manual_interventions(id,run_id,task_id,from_attempt_id,to_attempt_id,actor,reason_hash,tree_hash,created_ms) VALUES(?,?,?,?,?,?,?,?,1)`,
		hexID(3002), sd.RunID, sd.TaskID, sd.AttemptID, next, "owner", reason, sd.StartHash)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, root)
	if n, err := s.ManualInterventions(ctx, sd.RunID); err != nil || n != 1 {
		t.Fatalf("%d interventions (%v)", n, err)
	}
	if from, ok, err := s.ReconcileInto(ctx, next); err != nil || !ok || from != sd.AttemptID {
		t.Fatalf("reconcile of %s, %v (%v)", from, ok, err)
	}
	// A row is one thing or the other.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO manual_interventions(id,run_id,actor,reason_hash,created_ms) VALUES(?,?,'owner',?,1)`, hexID(3003), sd.RunID, reason); err == nil {
		t.Fatal("a row that names neither a reconcile nor a delivery")
	}
}
