package state

import (
	"errors"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func TestCreateRunCarriesOnlyAnAcceptedAttemptOfAnArchivedRunOfThePlan(t *testing.T) {
	s := openStore(t, newRoot(t))
	sd := seedProjectRunTaskAttempt(t, s, 0)
	var plan, brief, constraints, spec string
	if err := s.db.QueryRow(`SELECT r.plan_hash, t.brief_hash, t.constraints_hash, t.spec_hash FROM runs r JOIN tasks t ON t.run_id=r.id WHERE r.id=?`, sd.RunID).
		Scan(&plan, &brief, &constraints, &spec); err != nil {
		t.Fatal(err)
	}
	n := 0
	create := func(planHash, taskID, carried string) error {
		n++
		return s.CreateRun(ctx, RunSpec{RunID: hexID(700 + n), CheckoutID: sd.CheckoutID, CanonicalRoot: "/repo-0", GitCommonDir: "/repo-0/.git", ManifestRoot: ".",
			PlanHash: planHash, ConfigHash: sd.PacketHash, Tasks: []TaskRow{{ID: taskID, Ordinal: 1, BriefHash: brief, ConstraintsHash: constraints, SpecHash: spec, AcceptedAttemptID: carried}}})
	}

	if err := create(plan, sd.TaskID, sd.AttemptID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an attempt that was never accepted: %v", err)
	}
	sd.setAttempt(t, `accepted_ms=1`)
	if err := create(plan, sd.TaskID, sd.AttemptID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an attempt of a run that is still running: %v", err)
	}
	sd.exec(t, `UPDATE runs SET state='archived', archived_ms=2 WHERE id=?`, sd.RunID)
	if err := create(sd.StartHash, sd.TaskID, sd.AttemptID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an attempt of another plan's run: %v", err)
	}
	if err := create(plan, "task-two", sd.AttemptID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an attempt of another task: %v", err)
	}
	if err := create(plan, sd.TaskID, hexID(999)); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an attempt that does not exist: %v", err)
	}
	if err := create(plan, sd.TaskID, sd.AttemptID); err != nil {
		t.Fatalf("an accepted attempt of an archived run of the plan: %v", err)
	}
	tasks, err := s.RunTasks(ctx, hexID(700+n))
	if err != nil || len(tasks) != 1 || tasks[0].AcceptedAttemptID != sd.AttemptID {
		t.Fatalf("carried task %+v %v", tasks, err)
	}

	if id, ok, err := s.LatestArchivedRun(ctx, plan); err != nil || !ok || id != sd.RunID {
		t.Fatalf("latest archived run %q %v %v", id, ok, err)
	}
	if _, ok, err := s.LatestArchivedRun(ctx, sd.StartHash); err != nil || ok {
		t.Fatalf("another plan has no archived run: %v %v", ok, err)
	}
}
