-- Schema version 3: versioned reviewer handback. Additive only. A reviewer
-- sends review_started and review_result messages through the run's mailbox;
-- the controller stages the exact result bytes here as an immutable candidate
-- and acknowledges receipt. Applying a candidate to the run is a separate step
-- that waits for the reviewer to settle. Nothing in this file lets a message
-- name its own stage: the stage is registered with the reviewer job.
--
-- Rollback is not starting runs with --review-delivery mailbox on this binary.
-- The tables stay; an older tandem refuses a schema-3 store (NewerSchemaError).

-- The mandatory review stages. A reviewer job has exactly one.
CREATE TABLE review_stages (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id),
 stage TEXT NOT NULL CHECK(stage IN ('builder_review','independent_verification')),
 created_ms INTEGER NOT NULL
) STRICT;
-- A verified start: which session took the review, and when it must be done.
-- Delivery acknowledgement (effects.state) and this execution deadline are
-- separate on purpose: a start resolves delivery uncertainty, a stalled review
-- is governed by deadline_ms. session_id is what the reviewer said;
-- observed_session is what the transport saw, when it saw one.
CREATE TABLE review_starts (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id), effect_id TEXT NOT NULL REFERENCES effects(id),
 event_id TEXT NOT NULL UNIQUE, session_id TEXT, observed_session TEXT,
 started_ms INTEGER NOT NULL, deadline_ms INTEGER NOT NULL CHECK(deadline_ms>=started_ms)
) STRICT;
-- The exact bytes a reviewer submitted, as an artifact, bound to its reviewer
-- job, effect and attempt. One candidate per job: the same bytes again are a
-- duplicate and other bytes are a conflict, never a replacement.
CREATE TABLE review_candidates (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), attempt_id TEXT NOT NULL,
 job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id), effect_id TEXT NOT NULL REFERENCES effects(id),
 event_id TEXT NOT NULL UNIQUE, stage TEXT NOT NULL CHECK(stage IN ('builder_review','independent_verification')),
 payload_hash TEXT NOT NULL REFERENCES artifacts(hash),
 state TEXT NOT NULL DEFAULT 'staged' CHECK(state IN ('staged','applied','rejected')),
 created_ms INTEGER NOT NULL,
 FOREIGN KEY(run_id,attempt_id) REFERENCES attempts(run_id,id)
) STRICT;

-- Run coordination for applying a late result. pause_generation counts pauses
-- (a new pause, or a pause for another cause, is a new one) and stop_generation
-- counts entries into stopping. pause_job_id and pause_effect_id name the
-- reviewer job and prompt effect a review_unknown pause waits on, so only that
-- job's result may clear it. A review candidate is applied under a fence made
-- of these, the run version, the lease and the reviewed tree.
ALTER TABLE runs ADD COLUMN pause_generation INTEGER NOT NULL DEFAULT 0 CHECK(pause_generation>=0);
ALTER TABLE runs ADD COLUMN stop_generation INTEGER NOT NULL DEFAULT 0 CHECK(stop_generation>=0);
ALTER TABLE runs ADD COLUMN pause_job_id TEXT;
ALTER TABLE runs ADD COLUMN pause_effect_id TEXT;
-- Why a candidate was refused, and when one was applied.
ALTER TABLE review_candidates ADD COLUMN reject_reason TEXT;
ALTER TABLE review_candidates ADD COLUMN applied_ms INTEGER;

-- Whether the reviewer of a result was a different party from the writer. It is
-- decided when the result is recorded, from the run's configuration and what
-- the transport observed, and only a distinct independent verification can
-- satisfy acceptance.
ALTER TABLE review_results ADD COLUMN distinct_from_writer INTEGER NOT NULL DEFAULT 0 CHECK(distinct_from_writer IN (0,1));
