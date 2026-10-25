package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// CreateRun records a run, its project and checkout rows when new, its tasks
// and their dependencies in one transaction. Dependencies must name tasks of
// the run and form no cycle. A run id used twice is ErrConflict.
func (s *Store) CreateRun(ctx context.Context, spec RunSpec) error {
	_, err := s.createRun(ctx, spec, "", 0)
	return err
}

// CreateRunAndLease is CreateRun that also takes the checkout's writer lease
// for owner in the same transaction, so a run is never created for a checkout
// that already has a writer. It returns the lease's fence; a checkout that is
// busy (or being reconciled) creates nothing and returns workflow.ErrBusy.
func (s *Store) CreateRunAndLease(ctx context.Context, spec RunSpec, owner string, ttl time.Duration) (int64, error) {
	if owner == "" {
		return 0, fmt.Errorf("%w: a lease needs an owner", workflow.ErrInvalidTransition)
	}
	return s.createRun(ctx, spec, owner, ttl)
}

func (s *Store) createRun(ctx context.Context, spec RunSpec, owner string, ttl time.Duration) (int64, error) {
	if s.readOnly {
		return 0, fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	if !idPattern.MatchString(spec.RunID) || !idPattern.MatchString(spec.CheckoutID) {
		return 0, fmt.Errorf("%w: run and checkout ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	}
	if !hashPattern.MatchString(spec.PlanHash) || !hashPattern.MatchString(spec.ConfigHash) || len(spec.Tasks) == 0 {
		return 0, fmt.Errorf("%w: a run needs plan and config hashes and at least one task", workflow.ErrInvalidTransition)
	}
	if err := checkTaskGraph(spec.Tasks); err != nil {
		return 0, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.rollback()
	now := s.now().UnixMilli()

	var existing string
	switch e := tx.conn.QueryRowContext(ctx, `SELECT id FROM checkouts WHERE canonical_root=?`, spec.CanonicalRoot).Scan(&existing); {
	case errors.Is(e, sql.ErrNoRows):
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?,?,?,?)`,
			spec.CheckoutID, spec.CanonicalRoot, spec.GitCommonDir, now); err != nil {
			return 0, err
		}
	case e != nil:
		return 0, e
	case existing != spec.CheckoutID:
		return 0, fmt.Errorf("%w: %s is recorded as checkout %s, not %s", workflow.ErrConflict, spec.CanonicalRoot, existing, spec.CheckoutID)
	}
	sum := sha256.Sum256([]byte("project|" + spec.CheckoutID + "|" + spec.ManifestRoot))
	projectID := hex.EncodeToString(sum[:16])
	if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO projects(id,checkout_id,manifest_root) VALUES(?,?,?)`, projectID, spec.CheckoutID, spec.ManifestRoot); err != nil {
		return 0, err
	}
	// The project may already exist under another id, as when a legacy import
	// created it; a run belongs to the one the checkout has.
	if err := tx.conn.QueryRowContext(ctx, `SELECT id FROM projects WHERE checkout_id=? AND manifest_root=?`, spec.CheckoutID, spec.ManifestRoot).Scan(&projectID); err != nil {
		return 0, err
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,task_input_limit,run_input_limit,created_ms)
		VALUES(?,?,?,?,2,'running',?,?,?)`, spec.RunID, projectID, spec.PlanHash, spec.ConfigHash, spec.TaskInputLimit, spec.RunInputLimit, now); err != nil {
		if isUnique(err) {
			return 0, fmt.Errorf("%w: run %s already exists", workflow.ErrConflict, spec.RunID)
		}
		return 0, err
	}
	refs := [][3]string{{"run", spec.RunID, "plan"}, {"run", spec.RunID, "config"}}
	hashes := []string{spec.PlanHash, spec.ConfigHash}
	for _, t := range spec.Tasks {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?,?,?,?,?,?)`,
			spec.RunID, t.ID, t.Ordinal, t.BriefHash, t.ConstraintsHash, t.SpecHash); err != nil {
			return 0, fmt.Errorf("task %s: %w", t.ID, err)
		}
		for _, r := range []struct{ role, h string }{{"brief", t.BriefHash}, {"constraints", t.ConstraintsHash}, {"spec", t.SpecHash}} {
			refs = append(refs, [3]string{"task", spec.RunID + "/" + t.ID, r.role})
			hashes = append(hashes, r.h)
		}
	}
	for _, t := range spec.Tasks {
		for _, dep := range t.DependsOn {
			if _, err := tx.conn.ExecContext(ctx, `INSERT INTO task_dependencies(run_id,task_id,predecessor_id) VALUES(?,?,?)`, spec.RunID, t.ID, dep); err != nil {
				return 0, fmt.Errorf("task %s depends on %s: %w", t.ID, dep, err)
			}
		}
	}
	for i, r := range refs {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES(?,?,?,?)`, r[0], r[1], r[2], hashes[i]); err != nil {
			return 0, err
		}
	}
	if err := insertImported(ctx, tx, spec, now); err != nil {
		return 0, err
	}
	var fence int64
	if owner != "" {
		var lerr error
		if fence, lerr = s.leaseTx(ctx, tx, spec.CheckoutID, owner, ttl); lerr != nil {
			if errors.Is(lerr, errLeaseFlipped) {
				lerr = fmt.Errorf("%w: the writer lease for checkout %s expired and is being reconciled", workflow.ErrBusy, spec.CheckoutID)
			}
			return 0, lerr
		}
	}
	return fence, tx.commit(ctx)
}

// checkTaskGraph validates ids, ordinals and dependencies, and rejects cycles.
func checkTaskGraph(tasks []TaskRow) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{workflow.ErrInvalidTransition}, a...)...)
	}
	byID := map[string]TaskRow{}
	ordinals := map[int]bool{}
	for _, t := range tasks {
		switch {
		case !workflow.ValidTaskID(t.ID):
			return bad("task id %q is not a task slug", t.ID)
		case t.Ordinal < 1 || ordinals[t.Ordinal]:
			return bad("task %s has a missing or repeated ordinal %d", t.ID, t.Ordinal)
		case !hashPattern.MatchString(t.BriefHash) || !hashPattern.MatchString(t.ConstraintsHash) || !hashPattern.MatchString(t.SpecHash):
			return bad("task %s needs brief, constraints and spec hashes", t.ID)
		}
		if _, dup := byID[t.ID]; dup {
			return bad("task %s is listed twice", t.ID)
		}
		byID[t.ID], ordinals[t.Ordinal] = t, true
	}
	for _, t := range tasks {
		for _, d := range t.DependsOn {
			if _, ok := byID[d]; !ok || d == t.ID {
				return bad("task %s depends on %q, which is not another task of the run", t.ID, d)
			}
		}
	}
	// Kahn: anything left after peeling tasks with no unmet dependency is a cycle.
	left := map[string]int{}
	for _, t := range tasks {
		left[t.ID] = len(t.DependsOn)
	}
	for progress := true; progress; {
		progress = false
		for _, t := range tasks {
			if n, ok := left[t.ID]; ok && n == 0 {
				delete(left, t.ID)
				progress = true
				for _, u := range tasks {
					for _, d := range u.DependsOn {
						if d == t.ID {
							left[u.ID]--
						}
					}
				}
			}
		}
	}
	if len(left) != 0 {
		var ids []string
		for id := range left {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return bad("the task dependencies form a cycle through %s", strings.Join(ids, ", "))
	}
	return nil
}

// CreateAttempt records a pending, current attempt at version 0. A task has at
// most one current attempt, so a repair must supersede the previous one first.
func (s *Store) CreateAttempt(ctx context.Context, a AttemptSpec) error {
	if err := validateAttemptSpec(a); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if err := s.createAttemptTx(ctx, tx, a); err != nil {
		return err
	}
	return tx.commit(ctx)
}

func validateAttemptSpec(a AttemptSpec) error {
	if !idPattern.MatchString(a.ID) || !idPattern.MatchString(a.RunID) || (a.ParentID != "" && !idPattern.MatchString(a.ParentID)) {
		return fmt.Errorf("%w: attempt, run and parent ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	}
	if a.Ordinal < 1 || !hashPattern.MatchString(a.StartSnapshotHash) || !hashPattern.MatchString(a.PacketHash) {
		return fmt.Errorf("%w: an attempt needs an ordinal, a start snapshot and a packet", workflow.ErrInvalidTransition)
	}
	if a.ManifestHash != "" && !hashPattern.MatchString(a.ManifestHash) {
		return fmt.Errorf("%w: the context manifest must be a 64 lowercase hex hash", workflow.ErrInvalidTransition)
	}
	return nil
}

// createAttemptTx inserts a validated attempt and its references inside tx.
func (s *Store) createAttemptTx(ctx context.Context, tx *dbTx, a AttemptSpec) error {
	var exists int
	if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE run_id=? AND id=?`, a.RunID, a.TaskID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%w: run %s has no task %q", workflow.ErrConflict, a.RunID, a.TaskID)
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO attempts(id,run_id,task_id,ordinal,parent_attempt_id,implementation_state,check_state,review_state,budget_state,
		start_snapshot_hash,packet_hash,version,is_current,created_ms) VALUES(?,?,?,?,?,'pending','pending','pending','available',?,?,0,1,?)`,
		a.ID, a.RunID, a.TaskID, a.Ordinal, nullable(a.ParentID), a.StartSnapshotHash, a.PacketHash, s.now().UnixMilli()); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: task %s already has a current attempt or this ordinal", workflow.ErrConflict, a.TaskID)
		}
		return err
	}
	refs := [][2]string{{"start_snapshot", a.StartSnapshotHash}, {"packet", a.PacketHash}}
	if a.ManifestHash != "" {
		refs = append(refs, [2]string{"context_manifest", a.ManifestHash})
	}
	for _, r := range a.Refs {
		if r[0] == "" || !hashPattern.MatchString(r[1]) {
			return fmt.Errorf("%w: an attempt reference needs a role and a 64 lowercase hex hash", workflow.ErrInvalidTransition)
		}
		refs = append(refs, r)
	}
	for _, r := range refs {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('attempt',?,?,?)`, a.ID, r[0], r[1]); err != nil {
			return err
		}
	}
	return nil
}

// AttemptManifest returns the context manifest an attempt's packet was built
// with, or false when it has none. It reads what was persisted; it never
// rebuilds a manifest from memory as it stands now.
func (s *Store) AttemptManifest(ctx context.Context, attemptID string) (string, bool, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM artifact_refs WHERE owner_kind='attempt' AND owner_id=? AND role='context_manifest'`, attemptID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}

// GetRun reads one run.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var pause, pauseJob, pauseEffect sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT r.id,r.project_id,p.checkout_id,r.state,r.pause_code,r.plan_hash,r.config_hash,r.version,r.task_input_limit,r.run_input_limit,
		r.pause_generation,r.stop_generation,r.pause_job_id,r.pause_effect_id
		FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.id=?`, id).
		Scan(&r.ID, &r.ProjectID, &r.CheckoutID, &r.State, &pause, &r.PlanHash, &r.ConfigHash, &r.Version, &r.TaskInputLimit, &r.RunInputLimit,
			&r.PauseGeneration, &r.StopGeneration, &pauseJob, &pauseEffect)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: run %s does not exist", workflow.ErrConflict, id)
	}
	r.PauseCode, r.PauseJobID, r.PauseEffectID = pause.String, pauseJob.String, pauseEffect.String
	return r, err
}

// RunTasks lists a run's tasks in order with their dependencies and whether an
// attempt has been accepted.
func (s *Store) RunTasks(ctx context.Context, runID string) ([]TaskRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,ordinal,brief_hash,constraints_hash,spec_hash,accepted_attempt_id FROM tasks WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	var out []TaskRow
	for rows.Next() {
		var t TaskRow
		var accepted sql.NullString
		if err := rows.Scan(&t.ID, &t.Ordinal, &t.BriefHash, &t.ConstraintsHash, &t.SpecHash, &accepted); err != nil {
			rows.Close()
			return nil, err
		}
		t.AcceptedAttemptID = accepted.String
		out = append(out, t)
	}
	rows.Close()
	for i := range out {
		deps, err := s.db.QueryContext(ctx, `SELECT predecessor_id FROM task_dependencies WHERE run_id=? AND task_id=? ORDER BY predecessor_id`, runID, out[i].ID)
		if err != nil {
			return nil, err
		}
		for deps.Next() {
			var d string
			if err := deps.Scan(&d); err != nil {
				deps.Close()
				return nil, err
			}
			out[i].DependsOn = append(out[i].DependsOn, d)
		}
		deps.Close()
	}
	return out, nil
}

// TaskAttempts lists every attempt of a task, oldest first.
func (s *Store) TaskAttempts(ctx context.Context, runID, taskID string) ([]workflow.Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM attempts WHERE run_id=? AND task_id=? ORDER BY ordinal`, runID, taskID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []workflow.Attempt
	for _, id := range ids {
		a, err := s.GetAttempt(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// SetRunState moves a run between running, paused, stopping and finished. A
// finished run stays finished, and nothing here archives one.
func (s *Store) SetRunState(ctx context.Context, runID, state, pauseCode string) error {
	switch state {
	case "running", "paused", "stopping", "finished":
	default:
		return fmt.Errorf("%w: SetRunState does not set %q", workflow.ErrInvalidTransition, state)
	}
	return s.setRunState(ctx, runID, state, pauseCode, "", "")
}

// SetRunPausedFor pauses a run for a cause that waits on one reviewer job and
// its prompt effect, so only that job's result can clear it. The pause counts
// as a new one unless the run is already paused for the same cause.
func (s *Store) SetRunPausedFor(ctx context.Context, runID, code, jobID, effectID string) error {
	return s.setRunState(ctx, runID, "paused", code, jobID, effectID)
}

func (s *Store) setRunState(ctx context.Context, runID, state, pauseCode, jobID, effectID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET
		pause_generation=pause_generation+CASE WHEN ?='paused' AND (state<>'paused' OR COALESCE(pause_code,'')<>?) THEN 1 ELSE 0 END,
		stop_generation=stop_generation+CASE WHEN ?='stopping' AND state<>'stopping' THEN 1 ELSE 0 END,
		pause_job_id=?, pause_effect_id=?, state=?, pause_code=?
		WHERE id=? AND state NOT IN ('finished','archived')`,
		state, pauseCode, state, nullable(jobID), nullable(effectID), state, nullable(pauseCode), runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: run %s does not exist or is finished", workflow.ErrConflict, runID)
	}
	return nil
}

// SetRunLimit persists one of a run's budget limits: scope "task" or "run", in
// total logical input tokens, 0 for none. It changes nothing else.
func (s *Store) SetRunLimit(ctx context.Context, runID, scope string, limit int64) error {
	if limit < 0 {
		return fmt.Errorf("%w: a budget limit cannot be negative", workflow.ErrInvalidTransition)
	}
	var column string
	switch scope {
	case "task":
		column = "task_input_limit"
	case "run":
		column = "run_input_limit"
	default:
		return fmt.Errorf("%w: the budget scope is task or run, not %q", workflow.ErrInvalidTransition, scope)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET `+column+`=? WHERE id=?`, limit, runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: run %s does not exist", workflow.ErrConflict, runID)
	}
	return nil
}
