-- Schema version 1. Connection pragmas, the transaction and PRAGMA user_version
-- are owned by the Go migrator (migrate.go); this file holds only the schema.
CREATE TABLE checkouts (
 id TEXT PRIMARY KEY, canonical_root TEXT NOT NULL UNIQUE,
 git_common_dir TEXT NOT NULL, created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE projects (
 id TEXT PRIMARY KEY, checkout_id TEXT NOT NULL REFERENCES checkouts(id),
 manifest_root TEXT NOT NULL, UNIQUE(checkout_id, manifest_root)
) STRICT;
CREATE TABLE artifacts (
 hash TEXT PRIMARY KEY CHECK(length(hash)=64), size_bytes INTEGER NOT NULL CHECK(size_bytes>=0),
 media_type TEXT NOT NULL, created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE runs (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
 plan_hash TEXT NOT NULL REFERENCES artifacts(hash), config_hash TEXT NOT NULL REFERENCES artifacts(hash),
 engine_version INTEGER NOT NULL CHECK(engine_version=2),
 state TEXT NOT NULL CHECK(state IN ('running','paused','stopping','finished','archived')),
 version INTEGER NOT NULL DEFAULT 0 CHECK(version>=0),
 task_input_limit INTEGER NOT NULL DEFAULT 0 CHECK(task_input_limit>=0),
 run_input_limit INTEGER NOT NULL DEFAULT 0 CHECK(run_input_limit>=0),
 created_ms INTEGER NOT NULL, pause_code TEXT, archived_ms INTEGER
) STRICT;
CREATE TABLE tasks (
 run_id TEXT NOT NULL REFERENCES runs(id), id TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>0),
 brief_hash TEXT NOT NULL REFERENCES artifacts(hash), constraints_hash TEXT NOT NULL REFERENCES artifacts(hash),
 spec_hash TEXT NOT NULL REFERENCES artifacts(hash), accepted_attempt_id TEXT,
 PRIMARY KEY(run_id,id), UNIQUE(run_id,ordinal)
) STRICT;
CREATE TABLE task_dependencies (
 run_id TEXT NOT NULL, task_id TEXT NOT NULL, predecessor_id TEXT NOT NULL,
 PRIMARY KEY(run_id,task_id,predecessor_id), CHECK(task_id<>predecessor_id),
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id),
 FOREIGN KEY(run_id,predecessor_id) REFERENCES tasks(run_id,id)
) STRICT;
CREATE TABLE attempts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL, task_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>0), parent_attempt_id TEXT REFERENCES attempts(id),
 implementation_state TEXT NOT NULL CHECK(implementation_state IN ('pending','running','submitted','stuck')),
 check_state TEXT NOT NULL CHECK(check_state IN ('pending','running','passed','failed','not_applicable','invalid')),
 review_state TEXT NOT NULL CHECK(review_state IN ('pending','running','passed','changes_requested','waived','not_applicable','invalid','failed')),
 budget_state TEXT NOT NULL CHECK(budget_state IN ('available','warning','exhausted','unknown')),
 start_snapshot_hash TEXT REFERENCES artifacts(hash), end_snapshot_hash TEXT REFERENCES artifacts(hash),
 patch_hash TEXT REFERENCES artifacts(hash), packet_hash TEXT REFERENCES artifacts(hash),
 version INTEGER NOT NULL DEFAULT 0 CHECK(version>=0), is_current INTEGER NOT NULL CHECK(is_current IN (0,1)),
 accepted_ms INTEGER, created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id), UNIQUE(run_id,task_id,ordinal), UNIQUE(run_id,id)
) STRICT;
CREATE UNIQUE INDEX one_current_attempt ON attempts(run_id,task_id) WHERE is_current=1;
CREATE TABLE events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 run_id TEXT NOT NULL REFERENCES runs(id), attempt_id TEXT,
 kind TEXT NOT NULL, payload_hash TEXT NOT NULL REFERENCES artifacts(hash),
 expected_version INTEGER NOT NULL CHECK(expected_version>=0),
 applied_version INTEGER NOT NULL CHECK(applied_version>=0), created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,attempt_id) REFERENCES attempts(run_id,id)
) STRICT;
CREATE TABLE jobs (
 id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id),
 kind TEXT NOT NULL CHECK(kind IN ('builder','check','reviewer','judge')),
 state TEXT NOT NULL CHECK(state IN ('pending','running','completed','failed','cancelling','cancelled','unknown')),
 input_hash TEXT NOT NULL REFERENCES artifacts(hash), config_hash TEXT NOT NULL REFERENCES artifacts(hash),
 patch_hash TEXT REFERENCES artifacts(hash), tree_hash TEXT REFERENCES artifacts(hash),
 result_hash TEXT REFERENCES artifacts(hash), runner_kind TEXT NOT NULL,
 session_id TEXT, started_ms INTEGER, completed_ms INTEGER, UNIQUE(attempt_id,id)
) STRICT;
CREATE TABLE effects (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), attempt_id TEXT,
 job_id TEXT REFERENCES jobs(id), source_event_id TEXT NOT NULL REFERENCES events(id),
 kind TEXT NOT NULL, ordinal INTEGER NOT NULL DEFAULT 0,
 payload_hash TEXT NOT NULL REFERENCES artifacts(hash), nonce TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','delivering','acknowledged','delivery_unknown','failed','cancelled')),
 lease_owner TEXT, lease_until_ms INTEGER, fence INTEGER NOT NULL DEFAULT 0 CHECK(fence>=0),
 receipt_hash TEXT REFERENCES artifacts(hash), retry_count INTEGER NOT NULL DEFAULT 0 CHECK(retry_count>=0),
 available_ms INTEGER NOT NULL, created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,attempt_id) REFERENCES attempts(run_id,id), UNIQUE(source_event_id,ordinal)
) STRICT;
CREATE INDEX pending_effects ON effects(state,available_ms,created_ms);
CREATE TABLE check_certificates (
 id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),
 before_inputs_hash TEXT NOT NULL REFERENCES artifacts(hash), after_inputs_hash TEXT NOT NULL REFERENCES artifacts(hash),
 command_hash TEXT NOT NULL REFERENCES artifacts(hash), environment_hash TEXT NOT NULL REFERENCES artifacts(hash),
 exit_code INTEGER NOT NULL, log_hash TEXT NOT NULL REFERENCES artifacts(hash),
 complete INTEGER NOT NULL CHECK(complete IN (0,1)), created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE review_results (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id), attempt_id TEXT NOT NULL REFERENCES attempts(id),
 result_hash TEXT NOT NULL REFERENCES artifacts(hash), patch_hash TEXT NOT NULL REFERENCES artifacts(hash),
 tree_hash TEXT NOT NULL REFERENCES artifacts(hash),
 status TEXT NOT NULL CHECK(status IN ('passed','changes_requested','failed')), created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE waivers (
 id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id),
 scope TEXT NOT NULL CHECK(scope IN ('implementation','check','review')),
 reason_hash TEXT NOT NULL REFERENCES artifacts(hash), actor TEXT NOT NULL,
 evidence_tree_hash TEXT NOT NULL REFERENCES artifacts(hash), created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE memory_entries (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('approved_decision','interface_contract','check_outcome','unresolved_issue','builder_claim')),
 scope_hash TEXT NOT NULL REFERENCES artifacts(hash), source_hash TEXT NOT NULL REFERENCES artifacts(hash),
 validity_hash TEXT NOT NULL REFERENCES artifacts(hash), value_hash TEXT NOT NULL REFERENCES artifacts(hash),
 supersedes_id TEXT REFERENCES memory_entries(id),
 trust TEXT NOT NULL CHECK(trust IN ('claim','observed','approved')),
 state TEXT NOT NULL CHECK(state IN ('active','superseded','invalid','conflicted')),
 required INTEGER NOT NULL CHECK(required IN (0,1)),
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id)
) STRICT;
CREATE TABLE memory_dependencies (
 memory_id TEXT NOT NULL REFERENCES memory_entries(id), run_id TEXT NOT NULL, task_id TEXT NOT NULL,
 PRIMARY KEY(memory_id,run_id,task_id), FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id)
) STRICT;
CREATE TABLE usage_records (
 id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), job_id TEXT NOT NULL REFERENCES jobs(id),
 source_id TEXT NOT NULL UNIQUE, model TEXT NOT NULL,
 input_tokens INTEGER CHECK(input_tokens>=0), cached_input_tokens INTEGER CHECK(cached_input_tokens>=0),
 output_tokens INTEGER CHECK(output_tokens>=0), duration_ms INTEGER CHECK(duration_ms>=0),
 completeness TEXT NOT NULL CHECK(completeness IN ('complete','partial','unavailable')),
 input_semantics TEXT NOT NULL CHECK(input_semantics IN ('includes_cache','excludes_cache','unknown')),
 CHECK(cached_input_tokens IS NULL OR input_tokens IS NULL OR cached_input_tokens<=input_tokens)
) STRICT;
CREATE TABLE artifact_refs (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, role TEXT NOT NULL,
 hash TEXT NOT NULL REFERENCES artifacts(hash), PRIMARY KEY(owner_kind,owner_id,role,hash)
) STRICT;
CREATE INDEX artifact_ref_hash ON artifact_refs(hash);
CREATE TABLE checkout_leases (
 checkout_id TEXT PRIMARY KEY REFERENCES checkouts(id), owner_id TEXT NOT NULL,
 expires_ms INTEGER NOT NULL, fence INTEGER NOT NULL CHECK(fence>=0),
 state TEXT NOT NULL CHECK(state IN ('owned','reconciling','released'))
) STRICT;
CREATE TABLE calibration_records (
 id TEXT PRIMARY KEY, policy_hash TEXT NOT NULL REFERENCES artifacts(hash),
 judge_input_hash TEXT NOT NULL REFERENCES artifacts(hash), review_result_hash TEXT REFERENCES artifacts(hash),
 status TEXT NOT NULL CHECK(status IN ('pending','paired','excluded','corrupt','miss')),
 metadata_hash TEXT NOT NULL REFERENCES artifacts(hash), created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE imports (
 id TEXT PRIMARY KEY, source_digest TEXT NOT NULL UNIQUE,
 run_id TEXT NOT NULL REFERENCES runs(id), manifest_hash TEXT NOT NULL REFERENCES artifacts(hash),
 completed_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE pins (
 id TEXT PRIMARY KEY, run_id TEXT REFERENCES runs(id), hash TEXT REFERENCES artifacts(hash),
 reason TEXT NOT NULL, CHECK(run_id IS NOT NULL OR hash IS NOT NULL)
) STRICT;
-- accepted_attempt_id is intentionally a nullable logical reference because
-- tasks precede attempts. Store acceptance validates same run/task atomically.
-- All blob FK references also get artifact_refs rows, checked by store tests.
-- GC must conservatively union direct FK references, artifact_refs and pins.
-- Go migration commits PRAGMA user_version=1 only after this script succeeds.
