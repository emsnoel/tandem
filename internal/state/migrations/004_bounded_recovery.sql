-- Bounded recovery: one automatic successor attempt per origin budget group.
--
-- A successor attempt is admitted in one transaction that consumes the
-- allowance of every origin budget group the task's logical work draws on,
-- supersedes the failed attempt and creates the successor. The admission row
-- is the idempotency record: the same request returns the same successor and
-- consumes nothing twice, and a second request for the same parent attempt is
-- refused. The allowance itself lives on budget_groups.repairs_used, so a
-- rename, split, merge or replacement run cannot reset it.
CREATE TABLE repair_admissions (
 id TEXT PRIMARY KEY,
 collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 logical_work_id TEXT NOT NULL REFERENCES logical_work(id),
 run_id TEXT NOT NULL, task_id TEXT NOT NULL,
 parent_attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id),
 successor_attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id),
 cause TEXT NOT NULL CHECK(cause IN ('review_changes','check_failure','stuck')),
 evidence_hash TEXT NOT NULL REFERENCES artifacts(hash),
 original_tree_hash TEXT NOT NULL REFERENCES artifacts(hash),
 current_tree_hash TEXT NOT NULL REFERENCES artifacts(hash),
 run_version INTEGER NOT NULL, stop_generation INTEGER NOT NULL, lease_fence INTEGER NOT NULL,
 created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id)
) STRICT;
CREATE TABLE repair_admission_groups (
 admission_id TEXT NOT NULL REFERENCES repair_admissions(id),
 budget_group_id TEXT NOT NULL REFERENCES budget_groups(id),
 PRIMARY KEY(admission_id,budget_group_id)
) STRICT;

-- One recovery incident per failure of a work item's attempt. The key is
-- derived from the collaboration, the work (or task, when unmapped), the
-- failed attempt and the kind of failure, so a duplicate failure event, a
-- restart or archived traffic finds the same row and asks the planner nothing
-- more. The row is written before anyone is notified.
CREATE TABLE recovery_incidents (
 key TEXT PRIMARY KEY,
 collaboration_id TEXT NOT NULL REFERENCES collaborations(id),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL REFERENCES attempts(id),
 logical_work_id TEXT REFERENCES logical_work(id),
 kind TEXT NOT NULL CHECK(kind<>''), reason TEXT NOT NULL CHECK(reason<>''),
 evidence_hash TEXT REFERENCES artifacts(hash),
 state TEXT NOT NULL CHECK(state IN ('open','notified','resolved')),
 decision_effect_id TEXT, resolution TEXT,
 created_ms INTEGER NOT NULL, notified_ms INTEGER, resolved_ms INTEGER,
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id)
) STRICT;
CREATE INDEX recovery_incident_attempt ON recovery_incidents(attempt_id);

-- A person's edit, handed to the run through an explicit reconcile. It is
-- counted as an intervention. The reconcile attempt starts from the task's
-- original tree and is checked and reviewed like any other; no repair
-- allowance is created or reset by it.
CREATE TABLE manual_interventions (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL,
 from_attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id),
 to_attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id),
 actor TEXT NOT NULL CHECK(actor<>''), reason_hash TEXT NOT NULL REFERENCES artifacts(hash),
 tree_hash TEXT NOT NULL REFERENCES artifacts(hash), created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id)
) STRICT;
