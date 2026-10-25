package state

import (
	"context"
	"database/sql"
	"sort"
)

// ArtifactUse is one artifact a run holds, with its size and the roles rows
// give it. Artifacts a snapshot or patch names inside their own bytes are not
// listed here; snapshot.Children finds those.
type ArtifactUse struct {
	Hash  string
	Size  int64
	Roles []string
}

// runHashes selects every artifact hash a run's rows name, in columns and in
// artifact_refs. A run's evidence is both: a column is the live pointer and a
// reference keeps it from collection, and an export or a collector must not
// depend on the two agreeing.
const runHashes = `
SELECT plan_hash, 'plan' FROM runs WHERE id=:r
UNION ALL SELECT config_hash, 'config' FROM runs WHERE id=:r
UNION ALL SELECT brief_hash, 'brief' FROM tasks WHERE run_id=:r
UNION ALL SELECT constraints_hash, 'constraints' FROM tasks WHERE run_id=:r
UNION ALL SELECT spec_hash, 'spec' FROM tasks WHERE run_id=:r
UNION ALL SELECT start_snapshot_hash, 'start_snapshot' FROM attempts WHERE run_id=:r AND start_snapshot_hash IS NOT NULL
UNION ALL SELECT end_snapshot_hash, 'end_snapshot' FROM attempts WHERE run_id=:r AND end_snapshot_hash IS NOT NULL
UNION ALL SELECT patch_hash, 'patch' FROM attempts WHERE run_id=:r AND patch_hash IS NOT NULL
UNION ALL SELECT packet_hash, 'packet' FROM attempts WHERE run_id=:r AND packet_hash IS NOT NULL
UNION ALL SELECT payload_hash, 'event' FROM events WHERE run_id=:r
UNION ALL SELECT payload_hash, 'effect_payload' FROM effects WHERE run_id=:r
UNION ALL SELECT receipt_hash, 'effect_receipt' FROM effects WHERE run_id=:r AND receipt_hash IS NOT NULL
UNION ALL SELECT not_sent_hash, 'effect_not_sent' FROM effects WHERE run_id=:r AND not_sent_hash IS NOT NULL
UNION ALL SELECT j.input_hash, 'job_input' FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r
UNION ALL SELECT j.config_hash, 'job_config' FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r
UNION ALL SELECT j.patch_hash, 'job_patch' FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r AND j.patch_hash IS NOT NULL
UNION ALL SELECT j.tree_hash, 'job_tree' FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r AND j.tree_hash IS NOT NULL
UNION ALL SELECT j.result_hash, 'job_result' FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r AND j.result_hash IS NOT NULL
UNION ALL SELECT c.before_inputs_hash, 'check_before' FROM check_certificates c JOIN attempts a ON a.id=c.attempt_id WHERE a.run_id=:r
UNION ALL SELECT c.after_inputs_hash, 'check_after' FROM check_certificates c JOIN attempts a ON a.id=c.attempt_id WHERE a.run_id=:r
UNION ALL SELECT c.command_hash, 'check_command' FROM check_certificates c JOIN attempts a ON a.id=c.attempt_id WHERE a.run_id=:r
UNION ALL SELECT c.environment_hash, 'check_environment' FROM check_certificates c JOIN attempts a ON a.id=c.attempt_id WHERE a.run_id=:r
UNION ALL SELECT c.log_hash, 'check_log' FROM check_certificates c JOIN attempts a ON a.id=c.attempt_id WHERE a.run_id=:r
UNION ALL SELECT r.result_hash, 'review_result' FROM review_results r JOIN attempts a ON a.id=r.attempt_id WHERE a.run_id=:r
UNION ALL SELECT r.patch_hash, 'review_patch' FROM review_results r JOIN attempts a ON a.id=r.attempt_id WHERE a.run_id=:r
UNION ALL SELECT r.tree_hash, 'review_tree' FROM review_results r JOIN attempts a ON a.id=r.attempt_id WHERE a.run_id=:r
UNION ALL SELECT w.reason_hash, 'waiver_reason' FROM waivers w JOIN attempts a ON a.id=w.attempt_id WHERE a.run_id=:r
UNION ALL SELECT w.evidence_tree_hash, 'waiver_tree' FROM waivers w JOIN attempts a ON a.id=w.attempt_id WHERE a.run_id=:r
UNION ALL SELECT scope_hash, 'memory_scope' FROM memory_entries WHERE run_id=:r
UNION ALL SELECT source_hash, 'memory_source' FROM memory_entries WHERE run_id=:r
UNION ALL SELECT validity_hash, 'memory_validity' FROM memory_entries WHERE run_id=:r
UNION ALL SELECT value_hash, 'memory_value' FROM memory_entries WHERE run_id=:r
UNION ALL SELECT hash, owner_kind||':'||role FROM artifact_refs WHERE
   (owner_kind='run' AND owner_id=:r) OR (owner_kind='task' AND substr(owner_id,1,33)=:r||'/')
   OR (owner_kind='attempt' AND owner_id IN (SELECT id FROM attempts WHERE run_id=:r))
   OR (owner_kind='event' AND owner_id IN (SELECT id FROM events WHERE run_id=:r))
   OR (owner_kind='effect' AND owner_id IN (SELECT id FROM effects WHERE run_id=:r))
   OR (owner_kind='job' AND owner_id IN (SELECT j.id FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r))
   OR (owner_kind='check_certificate' AND owner_id IN (SELECT c.id FROM check_certificates c JOIN attempts a ON a.id=c.attempt_id WHERE a.run_id=:r))
   OR (owner_kind='review_result' AND owner_id IN (SELECT j.id FROM jobs j JOIN attempts a ON a.id=j.attempt_id WHERE a.run_id=:r))
   OR (owner_kind='waiver' AND owner_id IN (SELECT w.id FROM waivers w JOIN attempts a ON a.id=w.attempt_id WHERE a.run_id=:r))
   OR (owner_kind='memory' AND owner_id IN (SELECT id FROM memory_entries WHERE run_id=:r))
`

// RunArtifacts lists every artifact the run's rows name, once each, in hash
// order.
func (s *Store) RunArtifacts(ctx context.Context, runID string) ([]ArtifactUse, error) {
	rows, err := s.db.QueryContext(ctx, `WITH h(hash, role) AS (`+runHashes+`)
		SELECT h.hash, a.size_bytes, h.role FROM h JOIN artifacts a ON a.hash=h.hash ORDER BY h.hash, h.role`, sql.Named("r", runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactUse
	for rows.Next() {
		var hash, role string
		var size int64
		if err := rows.Scan(&hash, &size, &role); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Hash == hash {
			if !contains(out[n-1].Roles, role) {
				out[n-1].Roles = append(out[n-1].Roles, role)
			}
			continue
		}
		out = append(out, ArtifactUse{Hash: hash, Size: size, Roles: []string{role}})
	}
	for i := range out {
		sort.Strings(out[i].Roles)
	}
	return out, rows.Err()
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
