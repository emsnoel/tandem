-- Schema version 2: the collaboration ledger. Additive only; no schema-1 table
-- or constraint changes, and attempt-bound usage_records stay as they are for
-- legacy reports. A collaboration spans planning, every run and replacement
-- run, reviews, repairs and closeout.
--
-- Rollback is disabling the opt-in collaboration flags on this binary. Do not
-- downgrade a migrated database: an older tandem refuses schema 2 by design
-- (NewerSchemaError) and the backup taken before this step is the only copy at
-- schema 1.
-- status is the lifecycle; admission is the stop switch. A stop sets admission
-- to stopped and increments stop_generation in one transaction, so a dispatch
-- admission computed before it cannot commit after it. Only an audited resume
-- reopens admission, and a closing collaboration never resumes.
CREATE TABLE collaborations (
 id TEXT PRIMARY KEY, policy_hash TEXT NOT NULL REFERENCES artifacts(hash),
 status TEXT NOT NULL CHECK(status IN ('open','closing','closed')),
 admission TEXT NOT NULL DEFAULT 'open' CHECK(admission IN ('open','stopped')),
 budget_enforced INTEGER NOT NULL DEFAULT 0 CHECK(budget_enforced IN (0,1)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>=1),
 stop_generation INTEGER NOT NULL DEFAULT 0 CHECK(stop_generation>=0),
 created_ms INTEGER NOT NULL, closed_ms INTEGER
) STRICT;
-- Scheduling ceilings per provider. A null ceiling was not declared, zero
-- allows nothing, and unlimited is an explicit declaration that differs from a
-- missing one. input_reservation and output_reservation are what each launch
-- reserves. Spent and reserved counters are maintained by dispatch admission;
-- overshoot is usage beyond a ceiling that an in-flight call could not avoid.
CREATE TABLE provider_budgets (
 collaboration_id TEXT NOT NULL REFERENCES collaborations(id), provider TEXT NOT NULL CHECK(provider<>''),
 unlimited INTEGER NOT NULL DEFAULT 0 CHECK(unlimited IN (0,1)),
 input_ceiling INTEGER CHECK(input_ceiling>=0), output_ceiling INTEGER CHECK(output_ceiling>=0),
 input_reservation INTEGER CHECK(input_reservation>=0), output_reservation INTEGER CHECK(output_reservation>=0),
 input_spent INTEGER NOT NULL DEFAULT 0 CHECK(input_spent>=0), output_spent INTEGER NOT NULL DEFAULT 0 CHECK(output_spent>=0),
 input_reserved INTEGER NOT NULL DEFAULT 0 CHECK(input_reserved>=0), output_reserved INTEGER NOT NULL DEFAULT 0 CHECK(output_reserved>=0),
 input_overshoot INTEGER NOT NULL DEFAULT 0 CHECK(input_overshoot>=0), output_overshoot INTEGER NOT NULL DEFAULT 0 CHECK(output_overshoot>=0),
 PRIMARY KEY(collaboration_id,provider)
) STRICT;
-- Logical work is the immutable identity of one unit of planned work. Plan
-- task names and run-local task ids are aliases and bindings, never identity.
CREATE TABLE logical_work (
 id TEXT PRIMARY KEY, collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 origin_name TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('active','superseded')),
 created_ms INTEGER NOT NULL, UNIQUE(collaboration_id,id)
) STRICT;
-- A budget group holds one origin's repair allowance. Only new origin work
-- creates a group; renames, splits, merges and replacement runs reuse them.
CREATE TABLE budget_groups (
 id TEXT PRIMARY KEY, collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 repair_allowance INTEGER NOT NULL DEFAULT 1 CHECK(repair_allowance>=0),
 repairs_used INTEGER NOT NULL DEFAULT 0 CHECK(repairs_used>=0 AND repairs_used<=repair_allowance),
 created_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE work_budget_groups (
 logical_work_id TEXT NOT NULL REFERENCES logical_work(id), budget_group_id TEXT NOT NULL REFERENCES budget_groups(id),
 PRIMARY KEY(logical_work_id,budget_group_id)
) STRICT;
CREATE TABLE work_ancestry (
 parent_work_id TEXT NOT NULL REFERENCES logical_work(id), child_work_id TEXT NOT NULL REFERENCES logical_work(id),
 relation TEXT NOT NULL CHECK(relation IN ('split','merge')), created_ms INTEGER NOT NULL,
 PRIMARY KEY(parent_work_id,child_work_id), CHECK(parent_work_id<>child_work_id)
) STRICT;
CREATE TABLE work_aliases (
 collaboration_id TEXT NOT NULL REFERENCES collaborations(id), alias TEXT NOT NULL CHECK(alias<>''),
 logical_work_id TEXT NOT NULL, created_ms INTEGER NOT NULL,
 PRIMARY KEY(collaboration_id,alias),
 FOREIGN KEY(collaboration_id,logical_work_id) REFERENCES logical_work(collaboration_id,id)
) STRICT;
CREATE INDEX work_alias_target ON work_aliases(logical_work_id);
-- A run's task belongs to at most one logical work; a replacement run binds its
-- own task to the same work.
CREATE TABLE work_task_bindings (
 run_id TEXT NOT NULL, task_id TEXT NOT NULL, logical_work_id TEXT NOT NULL REFERENCES logical_work(id),
 PRIMARY KEY(run_id,task_id), FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id)
) STRICT;
-- Expected sources are registered before launch. Pre-run sources (planning,
-- preflight review) have no run, attempt or job; session_id stays null until a
-- launch receipt binds it. collaboration_id is the launch owner, inherited by a
-- child from its parent and never changed. model is what was requested;
-- actual_model is what the provider reported, so a mismatch stays visible.
CREATE TABLE expected_sources (
 id TEXT PRIMARY KEY, collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 provider TEXT NOT NULL CHECK(provider<>''), model TEXT, actual_model TEXT, role TEXT NOT NULL CHECK(role<>''),
 session_id TEXT, parent_source_id TEXT REFERENCES expected_sources(id),
 run_id TEXT REFERENCES runs(id), attempt_id TEXT, job_id TEXT REFERENCES jobs(id),
 start_boundary TEXT,
 status TEXT NOT NULL CHECK(status IN ('expected','bound','terminal','abandoned')),
 created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,attempt_id) REFERENCES attempts(run_id,id)
) STRICT;
-- Message boundaries of a planner (or other long-lived) session owned by one
-- collaboration. start_after and end_through are provider message ids.
CREATE TABLE session_segments (
 id TEXT PRIMARY KEY, collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 source_id TEXT NOT NULL REFERENCES expected_sources(id),
 provider TEXT NOT NULL, session_id TEXT NOT NULL, role TEXT NOT NULL,
 start_after TEXT, end_through TEXT, opened_ms INTEGER NOT NULL, closed_at INTEGER
) STRICT;
CREATE UNIQUE INDEX one_active_planner_segment ON session_segments(provider,session_id)
 WHERE role='planner' AND closed_at IS NULL;
-- Immutable observer-shaped snapshots. The document is an artifact. A newer
-- revision sets superseded_by on the one it replaces, which invalidates any
-- verdict that referenced it.
CREATE TABLE usage_snapshots (
 id TEXT PRIMARY KEY CHECK(length(id)=64), collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 revision INTEGER NOT NULL CHECK(revision>=1), supersedes_id TEXT REFERENCES usage_snapshots(id),
 content_hash TEXT NOT NULL REFERENCES artifacts(hash), complete INTEGER NOT NULL CHECK(complete IN (0,1)),
 superseded_by TEXT REFERENCES usage_snapshots(id), invalidated_ms INTEGER,
 created_ms INTEGER NOT NULL, UNIQUE(collaboration_id,revision)
) STRICT;
-- Incremental collection, per provider session: the parser cursor, committed
-- in one transaction with the event revisions it produced, so a replay after a
-- crash is idempotent. Events are keyed by provider, session and event id; two
-- sources on one session (a pane review in the builder's session) share them.
CREATE TABLE usage_cursors (
 provider TEXT NOT NULL, session_id TEXT NOT NULL, cursor_json TEXT NOT NULL,
 offset_bytes INTEGER NOT NULL CHECK(offset_bytes>=0), prefix_hash TEXT NOT NULL, updated_ms INTEGER NOT NULL,
 PRIMARY KEY(provider,session_id)
) STRICT;
CREATE TABLE usage_events (
 provider TEXT NOT NULL, session_id TEXT NOT NULL, event_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>=0), first_cursor INTEGER NOT NULL, event_json TEXT NOT NULL,
 PRIMARY KEY(provider,session_id,event_id)
) STRICT;
CREATE TABLE usage_revisions (
 provider TEXT NOT NULL, session_id TEXT NOT NULL, event_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>=0), event_json TEXT NOT NULL, created_ms INTEGER NOT NULL,
 PRIMARY KEY(provider,session_id,event_id,revision)
) STRICT;
-- One admission per effect: the durable permission to send it, and the tokens
-- it reserves. reserved holds the reservation; settled moved it to spent with
-- final usage; unknown_tail keeps it because final usage is not known;
-- released returned it because the effect was certainly not delivered.
CREATE TABLE dispatch_admissions (
 id TEXT PRIMARY KEY, collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 effect_id TEXT NOT NULL UNIQUE, source_id TEXT NOT NULL REFERENCES expected_sources(id),
 provider TEXT NOT NULL, budget_group_id TEXT REFERENCES budget_groups(id),
 run_id TEXT REFERENCES runs(id), attempt_id TEXT, job_id TEXT,
 run_version INTEGER NOT NULL, stop_generation INTEGER NOT NULL, lease_fence INTEGER NOT NULL,
 input_reserved INTEGER NOT NULL CHECK(input_reserved>=0), output_reserved INTEGER NOT NULL CHECK(output_reserved>=0),
 input_used INTEGER CHECK(input_used>=0), output_used INTEGER CHECK(output_used>=0),
 state TEXT NOT NULL CHECK(state IN ('reserved','settled','unknown_tail','released')),
 created_ms INTEGER NOT NULL, settled_ms INTEGER
) STRICT;
CREATE TABLE collaboration_audit (
 id TEXT PRIMARY KEY, collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 action TEXT NOT NULL CHECK(action IN ('stop','resume','amend_budget')),
 provider TEXT, detail TEXT NOT NULL, actor TEXT NOT NULL CHECK(actor<>''), reason TEXT NOT NULL CHECK(reason<>''),
 stop_generation INTEGER NOT NULL, created_ms INTEGER NOT NULL
) STRICT;
