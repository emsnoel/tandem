-- The latest rejected handback per prompt, retained until the review succeeds.
-- A corrected payload gets a new notification; repeated reconciliation does not.
CREATE TABLE review_rejections (
 effect_id TEXT PRIMARY KEY REFERENCES effects(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 attempt_id TEXT NOT NULL,
 job_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 reason TEXT NOT NULL CHECK(reason<>''),
 notified INTEGER NOT NULL DEFAULT 0 CHECK(notified IN (0,1)),
 updated_seq INTEGER NOT NULL,
 FOREIGN KEY(run_id,attempt_id) REFERENCES attempts(run_id,id),
 FOREIGN KEY(attempt_id,job_id) REFERENCES jobs(attempt_id,id)
) STRICT;
CREATE INDEX review_rejections_run ON review_rejections(run_id,updated_seq);
