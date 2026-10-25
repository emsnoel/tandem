package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/workflow"
)

// CheckCertificate is the controller's record of one check run: what ran, on
// which inputs before and after, and how it ended. Only the controller writes
// it, and `check_completed` reads it back, so nothing a builder says can
// supply a check's outcome.
type CheckCertificate struct {
	ID, AttemptID, JobID string
	BeforeInputsHash     string
	AfterInputsHash      string
	CommandHash          string
	EnvironmentHash      string
	LogHash              string
	ExitCode             int
	// Complete is false when the inputs could not be captured exactly.
	Complete bool
}

var jobKinds = map[string]bool{"builder": true, "check": true, "reviewer": true, "judge": true}

// InsertJob records a job before it is launched, so its id can name the job's
// log and every artifact it produces. State defaults to running.
func (s *Store) InsertJob(ctx context.Context, j workflow.Job) error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	if j.State == "" {
		j.State = "running"
	}
	switch {
	case !idPattern.MatchString(j.ID) || !idPattern.MatchString(j.AttemptID):
		return fmt.Errorf("%w: job and attempt ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	case !jobKinds[j.Kind]:
		return fmt.Errorf("%w: unknown job kind %q", workflow.ErrInvalidTransition, j.Kind)
	case j.State != "pending" && j.State != "running":
		return fmt.Errorf("%w: a new job starts pending or running, not %q", workflow.ErrInvalidTransition, j.State)
	case !hashPattern.MatchString(j.InputHash) || !hashPattern.MatchString(j.ConfigHash) || j.RunnerKind == "":
		return fmt.Errorf("%w: a job needs an input hash, a config hash and a runner kind", workflow.ErrInvalidTransition)
	}
	for _, opt := range []string{j.PatchHash, j.TreeHash} {
		if opt != "" && !hashPattern.MatchString(opt) {
			return fmt.Errorf("%w: malformed hash %q", workflow.ErrInvalidTransition, opt)
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	started := any(nil)
	if j.State == "running" {
		started = s.now().UnixMilli()
	}
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,patch_hash,tree_hash,runner_kind,session_id,started_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.AttemptID, j.Kind, j.State, j.InputHash, j.ConfigHash,
		nullable(j.PatchHash), nullable(j.TreeHash), j.RunnerKind, nullable(j.SessionID), started); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: job %s already exists", workflow.ErrConflict, j.ID)
		}
		return err
	}
	for _, r := range [][2]string{{"input", j.InputHash}, {"config", j.ConfigHash}} {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('job',?,?,?)`, j.ID, r[0], r[1]); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

// FinishJob ends a job that produced no result: failed, cancelled or unknown.
// A job is completed only by its result being recorded.
func (s *Store) FinishJob(ctx context.Context, jobID, state string) error {
	if state != "failed" && state != "cancelled" && state != "unknown" {
		return fmt.Errorf("%w: FinishJob ends a job as failed, cancelled or unknown, not %q", workflow.ErrInvalidTransition, state)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET state=?, completed_ms=? WHERE id=? AND state IN ('pending','running')`,
		state, s.now().UnixMilli(), jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: job %s is not pending or running", workflow.ErrConflict, jobID)
	}
	return nil
}

// RecordCheckCertificate stores a check's certificate and completes its job in
// one transaction. The job must be a running check of the certificate's
// attempt; a job is completed once.
func (s *Store) RecordCheckCertificate(ctx context.Context, c CheckCertificate) error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	if !idPattern.MatchString(c.ID) || !idPattern.MatchString(c.JobID) || !idPattern.MatchString(c.AttemptID) {
		return fmt.Errorf("%w: certificate, job and attempt ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	}
	hashes := []string{c.BeforeInputsHash, c.AfterInputsHash, c.CommandHash, c.EnvironmentHash, c.LogHash}
	for _, h := range hashes {
		if !hashPattern.MatchString(h) {
			return fmt.Errorf("%w: malformed certificate hash %q", workflow.ErrInvalidTransition, h)
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()

	var kind, state, attempt string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT kind,state,attempt_id FROM jobs WHERE id=?`, c.JobID).Scan(&kind, &state, &attempt); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: job %s does not exist", workflow.ErrConflict, c.JobID)
	case err != nil:
		return err
	}
	if kind != "check" || attempt != c.AttemptID {
		return fmt.Errorf("%w: job %s is not a check job of attempt %s", workflow.ErrConflict, c.JobID, c.AttemptID)
	}
	if state != "running" && state != "pending" {
		return fmt.Errorf("%w: job %s is already %s", workflow.ErrConflict, c.JobID, state)
	}
	complete := 0
	if c.Complete {
		complete = 1
	}
	now := s.now().UnixMilli()
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO check_certificates(id,attempt_id,job_id,before_inputs_hash,after_inputs_hash,command_hash,environment_hash,exit_code,log_hash,complete,created_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.AttemptID, c.JobID, c.BeforeInputsHash, c.AfterInputsHash, c.CommandHash,
		c.EnvironmentHash, c.ExitCode, c.LogHash, complete, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: certificate %s or job %s already has one", workflow.ErrConflict, c.ID, c.JobID)
		}
		return err
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE jobs SET state='completed', result_hash=?, completed_ms=? WHERE id=?`, c.LogHash, now, c.JobID); err != nil {
		return err
	}
	for i, role := range []string{"before_inputs", "after_inputs", "command", "environment", "log"} {
		if _, err := tx.conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('check_certificate',?,?,?)`, c.ID, role, hashes[i]); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// dbTx is one IMMEDIATE transaction on a reserved connection.
type dbTx struct {
	conn *sql.Conn
	done bool
}

func (s *Store) begin(ctx context.Context) (*dbTx, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, mapBusy(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, mapBusy(err)
	}
	return &dbTx{conn: conn}, nil
}

func (t *dbTx) commit(ctx context.Context) error {
	if _, err := t.conn.ExecContext(ctx, `COMMIT`); err != nil {
		return mapBusy(err)
	}
	t.done = true
	return nil
}

// rollback ends an uncommitted transaction and releases the connection.
func (t *dbTx) rollback() {
	if !t.done {
		t.conn.ExecContext(context.Background(), `ROLLBACK`)
	}
	t.conn.Close()
}

// AdoptArtifact registers bytes already in the artifact store as a database
// artifact, so rows may reference them. The whole object is read and verified
// first; a hash that is not stored, or does not match its bytes, is refused.
// It is idempotent.
func (s *Store) AdoptArtifact(ctx context.Context, hash string) error {
	if s.readOnly {
		return fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	store, err := artifact.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	c, err := store.Read(ctx, hash, 0, 1)
	if err != nil {
		return fmt.Errorf("adopting artifact %s: %w", hash, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,?,?)`,
		hash, c.TotalBytes, c.MediaType, s.now().UnixMilli())
	return err
}

// GetCheckCertificate returns a stored certificate by id.
func (s *Store) GetCheckCertificate(ctx context.Context, id string) (CheckCertificate, bool, error) {
	var c CheckCertificate
	var complete int
	err := s.db.QueryRowContext(ctx, `SELECT id,attempt_id,job_id,before_inputs_hash,after_inputs_hash,command_hash,environment_hash,exit_code,log_hash,complete
		FROM check_certificates WHERE id=?`, id).Scan(&c.ID, &c.AttemptID, &c.JobID, &c.BeforeInputsHash, &c.AfterInputsHash,
		&c.CommandHash, &c.EnvironmentHash, &c.ExitCode, &c.LogHash, &complete)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckCertificate{}, false, nil
	}
	c.Complete = complete == 1
	return c, err == nil, err
}

// AttemptCheckCertificate returns the most recent check certificate of an
// attempt, or false when it has none.
func (s *Store) AttemptCheckCertificate(ctx context.Context, attemptID string) (CheckCertificate, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT c.id FROM check_certificates c JOIN jobs j ON j.id=c.job_id
		WHERE c.attempt_id=? AND j.kind='check' ORDER BY c.created_ms DESC, c.id DESC LIMIT 1`, attemptID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckCertificate{}, false, nil
	}
	if err != nil {
		return CheckCertificate{}, false, err
	}
	return s.GetCheckCertificate(ctx, id)
}
