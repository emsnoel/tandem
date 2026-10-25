package state

import (
	"errors"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// reviewJobEnv is a seeded run with a reviewer job, and a review prompt effect
// that is not yet bound to it.
func reviewJobEnv(t *testing.T) (*Store, *seed, string, string, string) {
	t.Helper()
	s, sd, _ := newSeededStore(t)
	job, eff := hexID(7001), hexID(7002)
	if err := s.InsertJob(ctx, workflow.Job{ID: job, AttemptID: sd.AttemptID, Kind: "reviewer", State: "running", InputHash: sd.PacketHash,
		PatchHash: sd.PacketHash, TreeHash: sd.PacketHash, ConfigHash: sd.PacketHash, RunnerKind: "pane"}); err != nil {
		t.Fatal(err)
	}
	sd.exec(t, `INSERT INTO events(id,run_id,kind,payload_hash,expected_version,applied_version,created_ms) VALUES(?,?,'x',?,0,1,1)`,
		hexID(7003), sd.RunID, sd.PayloadHash)
	sd.exec(t, `INSERT INTO effects(id,run_id,attempt_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms)
		VALUES(?,?,?,?, 'review_prompt',0,?,'n','delivering',1,1,1)`, eff, sd.RunID, sd.AttemptID, hexID(7003), sd.PacketHash)
	return s, sd, job, eff, sd.artifact(t, `{"status":"passed"}`)
}

func TestReviewMailboxStageComesFromTheRegisteredJob(t *testing.T) {
	s, sd, job, eff, payload := reviewJobEnv(t)
	req := ReviewCandidateRequest{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: eff, EventID: hexID(7010), PayloadHash: payload}
	if _, _, err := s.StageReviewCandidate(ctx, req); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a reviewer with no registered stage: %v", err)
	}
	if err := s.RegisterReviewStage(ctx, job, StageIndependentVerification); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterReviewStage(ctx, job, StageIndependentVerification); err != nil {
		t.Fatalf("registering the same stage again: %v", err)
	}
	if err := s.RegisterReviewStage(ctx, job, StageBuilderReview); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a job serving two stages: %v", err)
	}
	if _, _, err := s.StageReviewCandidate(ctx, req); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("an effect not bound to the job: %v", err)
	}
	if err := s.BindEffectJob(ctx, eff, job); err != nil {
		t.Fatal(err)
	}
	c, dup, err := s.StageReviewCandidate(ctx, req)
	if err != nil || dup || c.Stage != StageIndependentVerification || c.State != "staged" {
		t.Fatalf("candidate %+v dup=%v %v", c, dup, err)
	}
	if err := s.RegisterReviewStage(ctx, hexID(7999), StageBuilderReview); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a stage for a job that does not exist: %v", err)
	}
	if err := s.RegisterReviewStage(ctx, job, "self_review"); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a made-up stage: %v", err)
	}
}

func TestReviewMailboxCandidateIsImmutable(t *testing.T) {
	s, sd, job, eff, payload := reviewJobEnv(t)
	s.RegisterReviewStage(ctx, job, StageBuilderReview)
	s.BindEffectJob(ctx, eff, job)
	req := ReviewCandidateRequest{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: eff, EventID: hexID(7010), PayloadHash: payload}
	first, _, err := s.StageReviewCandidate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if again, dup, err := s.StageReviewCandidate(ctx, req); err != nil || !dup || again.ID != first.ID {
		t.Fatalf("the same request again: %+v dup=%v %v", again, dup, err)
	}
	other := req
	other.EventID = hexID(7011)
	if _, dup, err := s.StageReviewCandidate(ctx, other); err != nil || !dup {
		t.Fatalf("the same bytes under another event: dup=%v %v", dup, err)
	}
	other.PayloadHash = sd.artifact(t, `{"status":"failed"}`)
	if _, _, err := s.StageReviewCandidate(ctx, other); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("other bytes for the same job: %v", err)
	}
	if kept, _, _ := s.ReviewCandidateFor(ctx, job); kept.PayloadHash != payload {
		t.Fatal("a conflicting candidate replaced the first")
	}
	var refs int
	s.db.QueryRow(`SELECT count(*) FROM artifact_refs WHERE owner_kind='review_candidate' AND hash=?`, payload).Scan(&refs)
	if refs != 1 {
		t.Fatalf("%d artifact refs for the staged result", refs)
	}
}

func TestReviewMailboxStartDeadlineIsSeparateFromDelivery(t *testing.T) {
	s, _, job, eff, _ := reviewJobEnv(t)
	req := ReviewStartRequest{JobID: job, EffectID: eff, EventID: hexID(7020), SessionID: "s1", Deadline: 2 * time.Minute}
	got, dup, err := s.RecordReviewStart(ctx, req)
	if err != nil || dup || got.DeadlineMS-got.StartedMS != 120_000 {
		t.Fatalf("start %+v dup=%v %v", got, dup, err)
	}
	if eff, _ := s.GetEffect(ctx, eff); eff.State != "delivering" {
		t.Fatalf("recording a start changed the effect to %s: acknowledging is the controller's separate step", eff.State)
	}
	req.EventID = hexID(7021)
	if _, dup, err := s.RecordReviewStart(ctx, req); err != nil || !dup {
		t.Fatalf("a repeated start: dup=%v %v", dup, err)
	}
	req.SessionID = "s2"
	if _, _, err := s.RecordReviewStart(ctx, req); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a start from another session: %v", err)
	}
	if _, _, err := s.RecordReviewStart(ctx, ReviewStartRequest{JobID: job, EffectID: eff, EventID: hexID(7022)}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("a start with no deadline: %v", err)
	}
}

func TestReviewFenceIncludesEveryGeneration(t *testing.T) {
	current := ReviewFence{RunVersion: 7, PauseGeneration: 3, StopGeneration: 2, LeaseFence: 5, AttemptID: "a",
		PauseReason: "review_unknown", PauseJobID: "j", TreeHash: "tree"}
	if !reviewFenceMatches(current, current) {
		t.Fatal("identical fence rejected")
	}
	for name, mutate := range map[string]func(*ReviewFence){
		"run version":      func(f *ReviewFence) { f.RunVersion++ },
		"pause generation": func(f *ReviewFence) { f.PauseGeneration++ },
		"stop generation":  func(f *ReviewFence) { f.StopGeneration++ },
		"lease fence":      func(f *ReviewFence) { f.LeaseFence++ },
		"attempt":          func(f *ReviewFence) { f.AttemptID = "b" },
		"pause reason":     func(f *ReviewFence) { f.PauseReason = "stopped" },
		"pause job":        func(f *ReviewFence) { f.PauseJobID = "k" },
		"tree":             func(f *ReviewFence) { f.TreeHash = "other" },
	} {
		changed := current
		mutate(&changed)
		if reviewFenceMatches(current, changed) || reviewFenceMatches(changed, current) {
			t.Errorf("a changed %s matched", name)
		}
	}
}

// pausedForReview is a seeded run with a v3 reviewer job whose candidate is
// staged, paused as review_unknown for that job, with its checkout lease held.
type appliedEnv struct {
	s                 *Store
	sd                *seed
	job, eff, payload string
	cand              ReviewCandidate
	lease             int64
}

func newAppliedEnv(t *testing.T) *appliedEnv {
	t.Helper()
	s, sd, job, eff, payload := reviewJobEnv(t)
	if err := s.RegisterReviewStage(ctx, job, StageBuilderReview); err != nil {
		t.Fatal(err)
	}
	if err := s.BindEffectJob(ctx, eff, job); err != nil {
		t.Fatal(err)
	}
	sd.exec(t, `UPDATE effects SET state='delivery_unknown' WHERE id=?`, eff)
	sd.exec(t, `UPDATE attempts SET end_snapshot_hash=?, patch_hash=? WHERE id=?`, sd.PacketHash, sd.PacketHash, sd.AttemptID)
	cand, _, err := s.StageReviewCandidate(ctx, ReviewCandidateRequest{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: eff,
		EventID: hexID(7050), PayloadHash: payload})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, sd.CheckoutID, "controller", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunPausedFor(ctx, sd.RunID, "review_unknown", job, eff); err != nil {
		t.Fatal(err)
	}
	return &appliedEnv{s: s, sd: sd, job: job, eff: eff, payload: payload, cand: cand, lease: lease}
}

func (a *appliedEnv) request(t *testing.T) ReviewApplyRequest {
	t.Helper()
	f, err := a.s.CurrentReviewFence(ctx, a.sd.RunID, a.sd.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return ReviewApplyRequest{CandidateID: a.cand.ID, RunID: a.sd.RunID, AttemptID: a.sd.AttemptID, JobID: a.job, EffectID: a.eff,
		PatchHash: a.sd.PacketHash, TreeHash: f.TreeHash, PauseReason: f.PauseReason, PauseJobID: f.PauseJobID,
		RunVersion: f.RunVersion, PauseGeneration: f.PauseGeneration, StopGeneration: f.StopGeneration, LeaseFence: f.LeaseFence}
}

func (a *appliedEnv) runState(t *testing.T) (string, string) {
	t.Helper()
	r, err := a.s.GetRun(ctx, a.sd.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return r.State, r.PauseCode
}

func TestReviewCandidateApplyClearsOnlyTheMatchingPause(t *testing.T) {
	a := newAppliedEnv(t)
	res, err := a.s.ApplyReviewCandidate(ctx, a.request(t))
	if err != nil || !res.Applied || !res.ClearedPause {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if st, code := a.runState(t); st != "running" || code != "" {
		t.Fatalf("run %s/%s after the matching pause was cleared", st, code)
	}
	if eff, _ := a.s.GetEffect(ctx, a.eff); eff.State != "acknowledged" {
		t.Fatalf("the prompt effect is %s", eff.State)
	}
	if c, _, _ := a.s.ReviewCandidateFor(ctx, a.job); c.State != "applied" {
		t.Fatalf("candidate %s", c.State)
	}
	// A replay converges: still applied, nothing cleared a second time.
	again, err := a.s.ApplyReviewCandidate(ctx, a.request(t))
	if err != nil || !again.Applied || again.ClearedPause {
		t.Fatalf("replay: %+v %v", again, err)
	}
}

func TestReviewCandidateApplyNeverClearsAnotherPause(t *testing.T) {
	for _, tc := range []struct{ code, job string }{
		{"scheduling_blocked", ""}, {"source_mutated", ""}, {"dispatch_failed", ""}, {"writer_unknown", ""},
		{"review_unknown", hexID(7777)}, // review_unknown, but for another job
	} {
		a := newAppliedEnv(t)
		if err := a.s.SetRunPausedFor(ctx, a.sd.RunID, tc.code, tc.job, ""); err != nil {
			t.Fatal(err)
		}
		res, err := a.s.ApplyReviewCandidate(ctx, a.request(t))
		if err != nil || !res.Applied || res.ClearedPause {
			t.Errorf("%s/%s: %+v %v", tc.code, tc.job, res, err)
		}
		if st, code := a.runState(t); st != "paused" || code != tc.code {
			t.Errorf("%s/%s: the run is %s/%s: a review result cleared an unrelated pause", tc.code, tc.job, st, code)
		}
	}
}

// A request built from an earlier read is refused when any part of the fence
// has moved, and nothing is written: the candidate stays staged and the pause
// stays.
func TestReviewCandidateApplyIsGuardedByEveryFence(t *testing.T) {
	moves := map[string]func(*appliedEnv){
		"a stop": func(a *appliedEnv) {
			a.sd.exec(t, `UPDATE runs SET state='stopping', stop_generation=stop_generation+1, version=version+1`)
		},
		"a new pause": func(a *appliedEnv) {
			a.s.SetRunPausedFor(ctx, a.sd.RunID, "review_unknown", a.job, a.eff)
			a.s.SetRunState(ctx, a.sd.RunID, "running", "")
			a.s.SetRunPausedFor(ctx, a.sd.RunID, "review_unknown", a.job, a.eff)
		},
		"the run version": func(a *appliedEnv) { a.sd.exec(t, `UPDATE runs SET version=version+1`) },
		"a lost lease":    func(a *appliedEnv) { a.sd.exec(t, `UPDATE checkout_leases SET state='released'`) },
		"a new lease":     func(a *appliedEnv) { a.sd.exec(t, `UPDATE checkout_leases SET fence=fence+1`) },
		"the attempt":     func(a *appliedEnv) { a.sd.exec(t, `UPDATE attempts SET is_current=0`) },
		"the tree":        func(a *appliedEnv) { a.sd.exec(t, `UPDATE attempts SET end_snapshot_hash=?`, a.sd.StartHash) },
	}
	for name, move := range moves {
		a := newAppliedEnv(t)
		req := a.request(t)
		move(a)
		res, err := a.s.ApplyReviewCandidate(ctx, req)
		if err != nil || res.Applied || res.ClearedPause {
			t.Errorf("%s: %+v %v: a stale request must be a quiet no-op", name, res, err)
		}
		if c, _, _ := a.s.ReviewCandidateFor(ctx, a.job); c.State != "staged" {
			t.Errorf("%s: candidate is %s", name, c.State)
		}
		if eff, _ := a.s.GetEffect(ctx, a.eff); eff.State != "delivery_unknown" {
			t.Errorf("%s: the effect is %s: nothing may be written on a miss", name, eff.State)
		}
	}
}

func TestReviewCandidateApplyRefusesBadRequests(t *testing.T) {
	a := newAppliedEnv(t)
	for name, mutate := range map[string]func(*ReviewApplyRequest){
		"another candidate": func(r *ReviewApplyRequest) { r.CandidateID = hexID(7999) },
		"another job":       func(r *ReviewApplyRequest) { r.JobID = hexID(7998) },
		"another patch":     func(r *ReviewApplyRequest) { r.PatchHash = a.payload },
	} {
		req := a.request(t)
		mutate(&req)
		if res, _ := a.s.ApplyReviewCandidate(ctx, req); res.Applied {
			t.Errorf("%s applied", name)
		}
	}
	if err := a.s.RejectReviewCandidate(ctx, a.cand.ID, "the result is not a review"); err != nil {
		t.Fatal(err)
	}
	if res, _ := a.s.ApplyReviewCandidate(ctx, a.request(t)); res.Applied {
		t.Error("a rejected candidate was applied")
	}
	if staged, _ := a.s.StagedReviewCandidates(ctx, a.sd.RunID); len(staged) != 0 {
		t.Errorf("a rejected candidate is still listed as staged: %+v", staged)
	}
}

func TestReviewRunGenerationsCountPausesAndStops(t *testing.T) {
	s, sd, _ := newSeededStore(t)
	gen := func() (int64, int64) {
		r, err := s.GetRun(ctx, sd.RunID)
		if err != nil {
			t.Fatal(err)
		}
		return r.PauseGeneration, r.StopGeneration
	}
	if p, st := gen(); p != 0 || st != 0 {
		t.Fatalf("a new run starts at %d/%d", p, st)
	}
	s.SetRunState(ctx, sd.RunID, "paused", "review_unknown")
	s.SetRunState(ctx, sd.RunID, "paused", "review_unknown") // the same pause again
	if p, _ := gen(); p != 1 {
		t.Fatalf("pause generation %d after one pause set twice", p)
	}
	s.SetRunState(ctx, sd.RunID, "paused", "source_mutated") // another cause
	s.SetRunState(ctx, sd.RunID, "running", "")
	s.SetRunState(ctx, sd.RunID, "paused", "source_mutated")
	if p, _ := gen(); p != 3 {
		t.Fatalf("pause generation %d after three distinct pauses", p)
	}
	s.SetRunState(ctx, sd.RunID, "stopping", "")
	s.SetRunState(ctx, sd.RunID, "stopping", "")
	if _, st := gen(); st != 1 {
		t.Fatalf("stop generation %d: entering stopping twice is one stop", st)
	}
}

// A pane review's prompt is bound to no job, so its review_unknown pause
// names only the prompt. Resolving that prompt, as a review recorded by hand
// or a review waiver does, clears it; a pause on another prompt or another
// job stays, whatever names this one.
func TestResolveReviewPromptClearsOnlyThePauseOnIt(t *testing.T) {
	for _, tc := range []struct {
		name, job, eff string
		clears         bool
	}{
		{"its own job", "self", "", true},
		{"no job, its own prompt", "", "self", true},
		{"no job, another prompt", "", hexID(7778), false},
		{"another job", hexID(7777), "self", false},
	} {
		a := newAppliedEnv(t)
		job, eff := tc.job, tc.eff
		if job == "self" {
			job = a.job
		}
		if eff == "self" {
			eff = a.eff
		}
		sd := a.sd
		sd.exec(t, `UPDATE runs SET pause_job_id=?, pause_effect_id=? WHERE id=?`, nullable(job), nullable(eff), sd.RunID)
		cleared, err := a.s.ResolveReviewPrompt(ctx, sd.RunID, a.job, a.eff, "acknowledged")
		if err != nil || cleared != tc.clears {
			t.Errorf("%s: cleared %v, %v; want %v", tc.name, cleared, err, tc.clears)
		}
		want := "paused"
		if tc.clears {
			want = "running"
		}
		if st, _ := a.runState(t); st != want {
			t.Errorf("%s: run is %s, want %s", tc.name, st, want)
		}
		if eff, _ := a.s.GetEffect(ctx, a.eff); eff.State != "acknowledged" {
			t.Errorf("%s: the prompt is %s; a resolved prompt is acknowledged", tc.name, eff.State)
		}
	}
	a := newAppliedEnv(t)
	if _, err := a.s.ResolveReviewPrompt(ctx, a.sd.RunID, a.job, a.eff, "delivered"); err == nil {
		t.Error("a prompt was resolved to a state that is not acknowledged or cancelled")
	}
}
