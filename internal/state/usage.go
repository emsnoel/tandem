package state

// Persistence for usage records. One row is one source: a logged event, or a
// marker saying a source could not be read in full. A source is unique, so
// collecting the same log again adds only what is new, and the run's total is
// the sum of the rows, never of anything read twice.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/workflow"
)

// UsageRecord is one source's usage for one job. SourceID binds provider,
// session, log and event, so the same event always has the same one.
type UsageRecord struct {
	ID, AttemptID, JobID, SourceID, Model string
	workflow.Usage
}

// UsageRow is a stored record with what attributing it needs.
type UsageRow struct {
	UsageRecord
	TaskID          string
	AttemptOrdinal  int
	ParentAttemptID string
	JobKind         string
	JobConfigHash   string
}

// PutUsage records one source after checking the figures and that the job
// belongs to the attempt. The same source with the same figures changes
// nothing; different figures are a conflict. clears names gap markers (partial
// or unavailable) to remove in the same step, for a log that has now been read.
//
// ErrUsageSourceTaken: another job already counts this source. A log line is
// counted once, for the first job that claimed it.
var ErrUsageSourceTaken = fmt.Errorf("%w: the usage source is already counted for another job", workflow.ErrConflict)

func (s *Store) PutUsage(ctx context.Context, r UsageRecord, clears ...string) error {
	switch {
	case !idPattern.MatchString(r.ID) || !idPattern.MatchString(r.AttemptID) || !idPattern.MatchString(r.JobID):
		return fmt.Errorf("%w: usage record, attempt and job ids must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	case !idPattern.MatchString(r.SourceID):
		return fmt.Errorf("%w: a usage source id must be 32 lowercase hex characters", workflow.ErrInvalidTransition)
	case r.Model == "":
		return fmt.Errorf("%w: a usage record names its model, or says it is unknown", workflow.ErrInvalidTransition)
	}
	if err := workflow.ValidateUsage(r.Usage); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()

	var jobAttempt string
	switch err := tx.conn.QueryRowContext(ctx, `SELECT attempt_id FROM jobs WHERE id=?`, r.JobID).Scan(&jobAttempt); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: job %s does not exist", workflow.ErrConflict, r.JobID)
	case err != nil:
		return err
	case jobAttempt != r.AttemptID:
		return fmt.Errorf("%w: job %s belongs to another attempt", workflow.ErrConflict, r.JobID)
	}

	var stored UsageRecord
	var in, cached, out sql.NullInt64
	switch err := tx.conn.QueryRowContext(ctx, `SELECT attempt_id,job_id,model,input_tokens,cached_input_tokens,output_tokens,duration_ms,completeness,input_semantics
		FROM usage_records WHERE source_id=?`, r.SourceID).
		Scan(&stored.AttemptID, &stored.JobID, &stored.Model, &in, &cached, &out, &stored.DurationMS, &stored.Completeness, &stored.InputSemantics); {
	case err == nil:
		stored.Input, stored.CachedInput, stored.Output = nullable64(in), nullable64(cached), nullable64(out)
		if stored.JobID != r.JobID {
			return fmt.Errorf("%w (source %s, job %s)", ErrUsageSourceTaken, r.SourceID, stored.JobID)
		}
		if !sameUsage(stored, r) {
			return fmt.Errorf("%w: source %s is already recorded with different figures", workflow.ErrConflict, r.SourceID)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO usage_records(id,attempt_id,job_id,source_id,model,input_tokens,cached_input_tokens,output_tokens,duration_ms,completeness,input_semantics)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.AttemptID, r.JobID, r.SourceID, r.Model, ptrArg(r.Input), ptrArg(r.CachedInput), ptrArg(r.Output),
			r.DurationMS, r.Completeness, r.InputSemantics); err != nil {
			return err
		}
	default:
		return err
	}
	for _, id := range clears {
		if _, err := tx.conn.ExecContext(ctx, `DELETE FROM usage_records WHERE source_id=? AND job_id=? AND completeness IN ('partial','unavailable')`, id, r.JobID); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

func nullable64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func ptrArg(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func eq64(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func sameUsage(a, b UsageRecord) bool {
	return a.AttemptID == b.AttemptID && a.JobID == b.JobID && a.Model == b.Model && eq64(a.Input, b.Input) && eq64(a.CachedInput, b.CachedInput) &&
		eq64(a.Output, b.Output) && a.DurationMS == b.DurationMS && a.Completeness == b.Completeness && a.InputSemantics == b.InputSemantics
}

// ListUsage returns every usage record of a run, ordered by source id, with the
// attempt and job each belongs to.
func (s *Store) ListUsage(ctx context.Context, runID string) ([]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,u.attempt_id,u.job_id,u.source_id,u.model,u.input_tokens,u.cached_input_tokens,u.output_tokens,u.duration_ms,
			u.completeness,u.input_semantics,a.task_id,a.ordinal,COALESCE(a.parent_attempt_id,''),j.kind,j.config_hash
		FROM usage_records u JOIN attempts a ON a.id=u.attempt_id JOIN jobs j ON j.id=u.job_id WHERE a.run_id=? ORDER BY u.source_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var in, cached, o sql.NullInt64
		if err := rows.Scan(&r.ID, &r.AttemptID, &r.JobID, &r.SourceID, &r.Model, &in, &cached, &o, &r.DurationMS, &r.Completeness, &r.InputSemantics,
			&r.TaskID, &r.AttemptOrdinal, &r.ParentAttemptID, &r.JobKind, &r.JobConfigHash); err != nil {
			return nil, err
		}
		r.Input, r.CachedInput, r.Output = nullable64(in), nullable64(cached), nullable64(o)
		out = append(out, r)
	}
	return out, rows.Err()
}

// EffectReceiptHash returns the artifact that records what a transport said when
// it accepted an effect, or false when it has none.
func (s *Store) EffectReceiptHash(ctx context.Context, effectID string) (string, bool, error) {
	var h sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT receipt_hash FROM effects WHERE id=?`, effectID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !h.Valid) {
		return "", false, nil
	}
	return h.String, err == nil, err
}

// RunCreatedMS returns when a run was created, in Unix milliseconds.
func (s *Store) RunCreatedMS(ctx context.Context, runID string) (int64, error) {
	var ms int64
	err := s.db.QueryRowContext(ctx, `SELECT created_ms FROM runs WHERE id=?`, runID).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: run %s does not exist", workflow.ErrConflict, runID)
	}
	return ms, err
}
