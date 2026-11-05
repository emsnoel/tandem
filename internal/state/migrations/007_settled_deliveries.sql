-- A person's statement that an agent never read a prompt is an intervention as
-- well. It names the effect and replaces no attempt, so a row is either a
-- reconcile (both attempts and the tree) or a settled delivery (the effect).
-- Nothing refers to this table, so it can be rebuilt in place.
CREATE TABLE manual_interventions_new (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT,
 from_attempt_id TEXT UNIQUE REFERENCES attempts(id),
 to_attempt_id TEXT UNIQUE REFERENCES attempts(id),
 effect_id TEXT UNIQUE REFERENCES effects(id),
 actor TEXT NOT NULL CHECK(actor<>''), reason_hash TEXT NOT NULL REFERENCES artifacts(hash),
 tree_hash TEXT REFERENCES artifacts(hash), created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,task_id) REFERENCES tasks(run_id,id),
 CHECK((effect_id IS NULL AND task_id IS NOT NULL AND from_attempt_id IS NOT NULL AND to_attempt_id IS NOT NULL AND tree_hash IS NOT NULL)
    OR (effect_id IS NOT NULL AND from_attempt_id IS NULL AND to_attempt_id IS NULL AND tree_hash IS NULL))
) STRICT;
INSERT INTO manual_interventions_new(id,run_id,task_id,from_attempt_id,to_attempt_id,actor,reason_hash,tree_hash,created_ms)
 SELECT id,run_id,task_id,from_attempt_id,to_attempt_id,actor,reason_hash,tree_hash,created_ms FROM manual_interventions;
DROP TABLE manual_interventions;
ALTER TABLE manual_interventions_new RENAME TO manual_interventions;
