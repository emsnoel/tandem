package state

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

func runSpec(t *testing.T, s *Store, n int) RunSpec {
	t.Helper()
	sd := &seed{s: s, artifacts: artifactReader(t, s)}
	return RunSpec{
		RunID: hexID(n), CheckoutID: hexID(n + 1), CanonicalRoot: "/repo", GitCommonDir: "/repo/.git", ManifestRoot: ".",
		PlanHash: sd.artifact(t, "plan"), ConfigHash: sd.artifact(t, "config"),
		Tasks: []TaskRow{
			{ID: "task-1", Ordinal: 1, BriefHash: sd.artifact(t, "b1"), ConstraintsHash: sd.artifact(t, "c"), SpecHash: sd.artifact(t, "s1")},
			{ID: "task-2", Ordinal: 2, BriefHash: sd.artifact(t, "b2"), ConstraintsHash: sd.artifact(t, "c"), SpecHash: sd.artifact(t, "s2"), DependsOn: []string{"task-1"}},
		},
	}
}

func TestCreateRunWritesRunTasksAndDependenciesTogether(t *testing.T) {
	s, _, _ := newSeededStore(t)
	spec := runSpec(t, s, 700)
	if err := s.CreateRun(ctx, spec); err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(ctx, spec.RunID)
	if err != nil || run.State != "running" || run.CheckoutID != spec.CheckoutID || run.Version != 0 || run.PlanHash != spec.PlanHash {
		t.Fatalf("%+v %v", run, err)
	}
	tasks, err := s.RunTasks(ctx, spec.RunID)
	if err != nil || len(tasks) != 2 || tasks[0].ID != "task-1" || tasks[1].ID != "task-2" || len(tasks[1].DependsOn) != 1 || tasks[1].DependsOn[0] != "task-1" {
		t.Fatalf("%+v %v", tasks, err)
	}
	if err := s.CreateRun(ctx, spec); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a run id used twice: %v", err)
	}
	// A bad spec leaves no partial run.
	bad := runSpec(t, s, 710)
	bad.Tasks[1].DependsOn = []string{"no-such-task"}
	if err := s.CreateRun(ctx, bad); err == nil {
		t.Fatal("a dependency on a task that does not exist")
	}
	if _, err := s.GetRun(ctx, bad.RunID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a failed CreateRun left a run: %v", err)
	}
	cyc := runSpec(t, s, 720)
	cyc.Tasks[0].DependsOn = []string{"task-2"}
	if err := s.CreateRun(ctx, cyc); err == nil {
		t.Fatal("a cyclic plan was stored")
	}
}

func TestCreateAttemptAllowsOneCurrentAttemptPerTask(t *testing.T) {
	s, _, _ := newSeededStore(t)
	spec := runSpec(t, s, 700)
	s.CreateRun(ctx, spec)
	sd := &seed{s: s, artifacts: artifactReader(t, s)}
	a := AttemptSpec{ID: hexID(800), RunID: spec.RunID, TaskID: "task-1", Ordinal: 1, StartSnapshotHash: sd.artifact(t, "snap"), PacketHash: sd.artifact(t, "packet")}
	if err := s.CreateAttempt(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAttempt(ctx, a.ID)
	if err != nil || !got.Current || got.Version != 0 || got.Implementation != workflow.ImplementationPending || got.PacketHash != a.PacketHash {
		t.Fatalf("%+v %v", got, err)
	}
	dup := a
	dup.ID = hexID(801)
	dup.Ordinal = 2
	if err := s.CreateAttempt(ctx, dup); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a second current attempt for one task: %v", err)
	}
	// After a repair supersedes the first, the next ordinal is allowed and linked.
	s.db.Exec(`UPDATE attempts SET is_current=0 WHERE id=?`, a.ID)
	dup.ParentID = a.ID
	if err := s.CreateAttempt(ctx, dup); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAttempt(ctx, dup.ID); got.ParentAttemptID != a.ID || got.Ordinal != 2 {
		t.Fatalf("%+v", got)
	}
	for name, mutate := range map[string]func(*AttemptSpec){
		"unknown task": func(x *AttemptSpec) { x.TaskID = "nope" },
		"bad id":       func(x *AttemptSpec) { x.ID = "zz" },
		"zero ordinal": func(x *AttemptSpec) { x.Ordinal = 0 },
	} {
		bad := AttemptSpec{ID: hexID(850), RunID: spec.RunID, TaskID: "task-2", Ordinal: 1, StartSnapshotHash: a.StartSnapshotHash, PacketHash: a.PacketHash}
		mutate(&bad)
		if err := s.CreateAttempt(ctx, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCheckoutLeaseGivesOneWriterAndNeverTakesOverAnExpiredOne(t *testing.T) {
	s, sd, c := effectStore(t)
	co := sd.CheckoutID
	ttl := 10 * time.Minute
	fence, err := s.AcquireLease(ctx, co, "owner-a", ttl)
	if err != nil || fence != 1 {
		t.Fatalf("%d %v", fence, err)
	}
	if again, err := s.AcquireLease(ctx, co, "owner-a", ttl); err != nil || again != fence {
		t.Fatalf("the owner renewing its own lease: %d %v", again, err)
	}
	if _, err := s.AcquireLease(ctx, co, "owner-b", ttl); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("a second writer: %v", err)
	}
	owned, err := s.LeaseOwnedBy(ctx, co, "owner-a", fence)
	if err != nil || !owned {
		t.Fatalf("%v %v", owned, err)
	}
	if owned, _ := s.LeaseOwnedBy(ctx, co, "owner-b", fence); owned {
		t.Fatal("another owner reads as the writer")
	}

	// The owner vanishes. Expiry is a chance to reconcile, not to take over.
	c.advance(ttl + time.Second)
	if _, err := s.AcquireLease(ctx, co, "owner-b", ttl); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("an expired lease was taken over: %v", err)
	}
	st, err := s.LeaseState(ctx, co)
	if err != nil || st.State != "reconciling" || st.Owner != "owner-a" {
		t.Fatalf("%+v %v", st, err)
	}
	if owned, _ := s.LeaseOwnedBy(ctx, co, "owner-a", fence); owned {
		t.Fatal("a reconciling lease still counts as an owned writer")
	}
	// Only an explicit release after reconciliation frees it.
	if err := s.ReleaseLease(ctx, co, "owner-b", fence); err == nil {
		t.Fatal("a non-owner released the lease")
	}
	if err := s.ReleaseLease(ctx, co, "owner-a", fence); err != nil {
		t.Fatal(err)
	}
	next, err := s.AcquireLease(ctx, co, "owner-b", ttl)
	if err != nil || next != fence+1 {
		t.Fatalf("after release: %d %v", next, err)
	}
	if err := s.ReleaseLease(ctx, co, "owner-a", fence); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("the old owner released the new lease: %v", err)
	}
}

func TestClaimEffectKindsRestrictsWhatIsClaimed(t *testing.T) {
	s, sd, c := effectStore(t)
	rawEffect(t, sd, hexID(1), hexID(11), workflow.EffectAdvanceRun, "pending", c.ms-5)
	rawEffect(t, sd, hexID(2), hexID(12), workflow.EffectCancel, "pending", c.ms-4)
	got, err := s.ClaimEffectKinds(ctx, "w", sd.RunID, []string{workflow.EffectCancel})
	if err != nil || got.ID != hexID(2) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.ClaimEffectKinds(ctx, "w", sd.RunID, []string{workflow.EffectCancel}); !errors.Is(err, ErrNoEffect) {
		t.Fatalf("an effect of another kind was claimed: %v", err)
	}
	if got, err := s.ClaimEffectKinds(ctx, "w", sd.RunID, []string{workflow.EffectAdvanceRun}); err != nil || got.ID != hexID(1) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.ClaimEffectKinds(ctx, "w", sd.RunID, nil); err == nil {
		t.Fatal("an empty kind list must be refused, not mean everything")
	}
}

func TestDerivedEffectsShareTheirParentsEvent(t *testing.T) {
	s, sd, _ := effectStore(t)
	parent := pendingEffect(t, s, sd, 1)
	prompt := sd.artifact(t, "a derived prompt")
	child, err := s.InsertDerivedEffect(ctx, parent, "review_prompt", prompt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEffect(ctx, child.ID)
	if err != nil || got.Kind != "review_prompt" || got.PayloadHash != prompt || got.State != "pending" || got.AttemptID != child.AttemptID {
		t.Fatalf("%+v %v", got, err)
	}
	var ordinal int
	var source, parentSource string
	s.db.QueryRow(`SELECT ordinal,source_event_id FROM effects WHERE id=?`, child.ID).Scan(&ordinal, &source)
	s.db.QueryRow(`SELECT source_event_id FROM effects WHERE id=?`, parent).Scan(&parentSource)
	if ordinal != 1 || source != parentSource || got.Nonce == "" || got.Nonce == (workflow.Effect{}).Nonce {
		t.Fatalf("ordinal %d source %s parent source %s", ordinal, source, parentSource)
	}
	// Deriving the same kind twice from one parent returns the same effect: a retry adds nothing.
	again, err := s.InsertDerivedEffect(ctx, parent, "review_prompt", prompt)
	if err != nil || again.ID != child.ID {
		t.Fatalf("%+v %v", again, err)
	}
	if n := sd.count(t, "effects"); n != 2 {
		t.Fatalf("%d effects", n)
	}
	if _, err := s.InsertDerivedEffect(ctx, hexID(9999), "x", prompt); err == nil {
		t.Fatal("a derived effect of an effect that does not exist")
	}
}

func TestSetRunStateRecordsPauseAndFinish(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	if err := s.SetRunState(ctx, sd.RunID, "paused", "writer_unknown"); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.GetRun(ctx, sd.RunID); run.State != "paused" || run.PauseCode != "writer_unknown" {
		t.Fatalf("%+v", run)
	}
	if err := s.SetRunState(ctx, sd.RunID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.GetRun(ctx, sd.RunID); run.State != "running" || run.PauseCode != "" {
		t.Fatalf("%+v", run)
	}
	if err := s.SetRunState(ctx, sd.RunID, "archived", ""); err == nil {
		t.Fatal("SetRunState may not archive a run")
	}
	if err := s.SetRunState(ctx, sd.RunID, "finished", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunState(ctx, sd.RunID, "running", ""); err == nil {
		t.Fatal("a finished run was resumed")
	}
}

func TestBreakLeaseOnlyFreesAReconcilingLease(t *testing.T) {
	s, sd, c := effectStore(t)
	co := sd.CheckoutID
	fence, _ := s.AcquireLease(ctx, co, "dead-owner", time.Minute)
	if err := s.BreakLease(ctx, co, fence); err == nil {
		t.Fatal("a live lease was broken")
	}
	c.advance(2 * time.Minute)
	s.AcquireLease(ctx, co, "recoverer", time.Minute) // marks it reconciling
	if err := s.BreakLease(ctx, co, fence+7); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("a wrong fence: %v", err)
	}
	if err := s.BreakLease(ctx, co, fence); err != nil {
		t.Fatal(err)
	}
	if next, err := s.AcquireLease(ctx, co, "recoverer", time.Minute); err != nil || next != fence+1 {
		t.Fatalf("%d %v", next, err)
	}
}

func TestAttemptEvidenceReviewResultAndWaiverRecording(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	end, patch := sd.artifact(t, "end"), sd.artifact(t, "patch")
	if err := s.RecordAttemptEvidence(ctx, sd.AttemptID, end, patch); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttemptEvidence(ctx, sd.AttemptID, end, patch); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
	if a, _ := s.GetAttempt(ctx, sd.AttemptID); a.EndSnapshotHash != end || a.PatchHash != patch {
		t.Fatalf("%+v", a)
	}
	if err := s.RecordAttemptEvidence(ctx, sd.AttemptID, "short", patch); err == nil {
		t.Error("a malformed hash")
	}
	sd.setAttempt(t, `implementation_state='submitted'`)
	if err := s.RecordAttemptEvidence(ctx, sd.AttemptID, sd.artifact(t, "other end"), patch); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("changed evidence after submission: %v", err)
	}

	job := sd.newJob(t, 5, "reviewer")
	s.InsertJob(ctx, job)
	rec := ReviewRecord{JobID: job.ID, AttemptID: sd.AttemptID, ResultHash: sd.artifact(t, "result"), PatchHash: patch, TreeHash: end, Status: "passed"}
	if err := s.RecordReviewResult(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReviewResult(ctx, rec); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("a job completed twice: %v", err)
	}
	checkJob := sd.newJob(t, 6, "check")
	s.InsertJob(ctx, checkJob)
	rec.JobID = checkJob.ID
	if err := s.RecordReviewResult(ctx, rec); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("a review result for a check job: %v", err)
	}

	w := Waiver{ID: hexID(8800), AttemptID: sd.AttemptID, Scope: "review", Actor: "planner", ReasonHash: sd.artifact(t, "because"), EvidenceTreeHash: end}
	if err := s.RecordWaiver(ctx, w); err != nil {
		t.Fatal(err)
	}
	stale := w
	stale.ID, stale.EvidenceTreeHash = hexID(8801), sd.artifact(t, "an older tree")
	if err := s.RecordWaiver(ctx, stale); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("a waiver for another tree: %v", err)
	}
	for name, mutate := range map[string]func(*Waiver){
		"no actor":      func(x *Waiver) { x.Actor = "" },
		"unknown scope": func(x *Waiver) { x.Scope = "everything" },
	} {
		bad := w
		bad.ID = hexID(8802)
		mutate(&bad)
		if err := s.RecordWaiver(ctx, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAttemptJobsAndCancelPendingEffects(t *testing.T) {
	s, sd, c := effectStore(t)
	j1, j2, j3 := sd.newJob(t, 1, "check"), sd.newJob(t, 2, "reviewer"), sd.newJob(t, 3, "check")
	s.InsertJob(ctx, j1)
	c.advance(time.Second)
	s.InsertJob(ctx, j3)
	s.InsertJob(ctx, j2)
	checks, err := s.AttemptJobs(ctx, sd.AttemptID, "check")
	if err != nil || len(checks) != 2 || checks[0].ID != j1.ID || checks[1].ID != j3.ID {
		t.Fatalf("%+v %v", checks, err)
	}
	if none, _ := s.AttemptJobs(ctx, sd.AttemptID, "judge"); len(none) != 0 {
		t.Fatalf("%+v", none)
	}

	rawEffect(t, sd, hexID(1), hexID(11), workflow.EffectBuilderDispatch, "pending", c.ms)
	rawEffect(t, sd, hexID(2), hexID(12), workflow.EffectCancel, "pending", c.ms)
	rawEffect(t, sd, hexID(3), hexID(13), workflow.EffectBuilderDispatch, "delivering", c.ms)
	n, err := s.CancelPendingEffects(ctx, sd.RunID, []string{workflow.EffectBuilderDispatch})
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	for id, want := range map[string]string{hexID(1): "cancelled", hexID(2): "pending", hexID(3): "delivering"} {
		if st, _, _ := stateOf(t, s, id); st != want {
			t.Errorf("%s is %s, want %s", id, st, want)
		}
	}
	if _, err := s.CancelPendingEffects(ctx, sd.RunID, nil); err == nil {
		t.Fatal("an empty kind list cancelled everything")
	}
}

func TestCreateRunAndLeaseTakesTheLeaseAtomically(t *testing.T) {
	s, _, c := effectStore(t)
	spec := runSpec(t, s, 700)
	fence, err := s.CreateRunAndLease(ctx, spec, "owner-a", time.Minute)
	if err != nil || fence != 1 {
		t.Fatalf("%d %v", fence, err)
	}
	if owned, _ := s.LeaseOwnedBy(ctx, spec.CheckoutID, "owner-a", fence); !owned {
		t.Fatal("the creating owner does not hold the lease")
	}
	// A second run on the same checkout is refused and creates nothing.
	second := runSpec(t, s, 700)
	second.RunID = hexID(7100)
	if _, err := s.CreateRunAndLease(ctx, second, "owner-b", time.Minute); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.GetRun(ctx, second.RunID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a refused run was created: %v", err)
	}
	// An expired lease is not taken over by a new run either.
	c.advance(2 * time.Minute)
	if _, err := s.CreateRunAndLease(ctx, second, "owner-b", time.Minute); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("a new run on an expired lease: %v", err)
	}
	if _, err := s.GetRun(ctx, second.RunID); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a refused run was created: %v", err)
	}
	if _, err := s.CreateRunAndLease(ctx, second, "", time.Minute); err == nil {
		t.Fatal("a lease with no owner")
	}
}

func TestRenewLeaseOnlyExtendsAHeldLease(t *testing.T) {
	s, sd, c := effectStore(t)
	co := sd.CheckoutID
	fence, _ := s.AcquireLease(ctx, co, "owner-a", time.Minute)
	c.advance(2 * time.Minute) // expired, but nobody has taken over
	if err := s.RenewLease(ctx, co, "owner-a", fence, time.Minute); err != nil {
		t.Fatalf("renewing one's own expired lease: %v", err)
	}
	if owned, _ := s.LeaseOwnedBy(ctx, co, "owner-a", fence); !owned {
		t.Fatal("not owned after renewal")
	}
	for name, err := range map[string]error{
		"wrong owner": s.RenewLease(ctx, co, "owner-b", fence, time.Minute),
		"wrong fence": s.RenewLease(ctx, co, "owner-a", fence+1, time.Minute),
	} {
		if !errors.Is(err, ErrStaleFence) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s.ReleaseLease(ctx, co, "owner-a", fence)
	if err := s.RenewLease(ctx, co, "owner-a", fence, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("a released lease was renewed: %v", err)
	}
	if st, _ := s.LeaseState(ctx, co); st.State != "released" {
		t.Fatalf("renewing changed the state to %s", st.State)
	}
}

func TestAttemptManifestIsReferencedAndReadBack(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	spec := runSpec(t, s, 700)
	if err := s.CreateRun(ctx, spec); err != nil {
		t.Fatal(err)
	}
	start, packet, manifest := sd.artifact(t, "start"), sd.artifact(t, "packet"), sd.artifact(t, "manifest")
	a := AttemptSpec{ID: hexID(710), RunID: spec.RunID, TaskID: "task-1", Ordinal: 1, StartSnapshotHash: start, PacketHash: packet, ManifestHash: manifest}
	if err := s.CreateAttempt(ctx, a); err != nil {
		t.Fatal(err)
	}
	if h, found, err := s.AttemptManifest(ctx, a.ID); err != nil || !found || h != manifest {
		t.Fatalf("%q %v %v", h, found, err)
	}
	// An attempt made without one reports none rather than an empty hash.
	b := AttemptSpec{ID: hexID(711), RunID: spec.RunID, TaskID: "task-2", Ordinal: 1, StartSnapshotHash: start, PacketHash: packet}
	if err := s.CreateAttempt(ctx, b); err != nil {
		t.Fatal(err)
	}
	if h, found, err := s.AttemptManifest(ctx, b.ID); err != nil || found || h != "" {
		t.Errorf("%q %v %v", h, found, err)
	}
	// A manifest must be a registered artifact like every other hash.
	c := AttemptSpec{ID: hexID(712), RunID: spec.RunID, TaskID: "task-1", Ordinal: 2, StartSnapshotHash: start, PacketHash: packet, ManifestHash: "zz"}
	if err := s.CreateAttempt(ctx, c); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("%v", err)
	}
}
