package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/emsnoel/tandem/internal/workflow"
)

// MaxAttempts is how many attempts one task gets: the first and two automatic
// repairs.
const MaxAttempts = 3

var (
	idPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// applyHooks fail Apply's transaction at named points, before COMMIT. Only
// tests set them.
type applyHooks struct {
	afterEvent, afterAttempt, afterEffects func() error
}

// EffectID and EffectNonce are the id and nonce the effect an event creates at
// a given position will have. A caller that must put the nonce into the
// prompt before the event is applied computes it here.
func EffectID(eventID string, ordinal int) string { return effectID(eventID, ordinal) }

// EffectNonce: see EffectID.
func EffectNonce(eventID string, ordinal int) string { return effectNonce(eventID, ordinal) }

// effectID and effectNonce derive an effect's identity from the event that
// caused it and its position, so an event that is retried after a failure
// produces the same effect rather than a second one.
func effectID(eventID string, ordinal int) string { return derive(eventID, "effect", ordinal) }

func effectNonce(eventID string, ordinal int) string { return derive(eventID, "nonce", ordinal) }

func derive(eventID, label string, ordinal int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", eventID, label, ordinal)))
	return hex.EncodeToString(sum[:16])
}

// Apply records one event and everything it causes in a single transaction.
// It supplies no controller-observed facts, so only events that need none
// succeed; the controller uses ApplyWith.
func (s *Store) Apply(ctx context.Context, ev workflow.Event) (workflow.Receipt, error) {
	return s.ApplyWith(ctx, ev, workflow.Observed{})
}

// ApplyWith is Apply with the facts only the controller can observe. Facts
// that live in the database (certificates, review results, waivers, task
// dependencies, attempt ordinal) are loaded here, inside the transaction,
// never taken from the event.
//
// Order inside BEGIN IMMEDIATE: a duplicate of an already applied event is
// answered first, with its original receipt, even when its expected version is
// stale. Then membership, currency of the attempt and the version are checked,
// the pure Reduce runs, and the event, the attempt update and the effects are
// written, or none of them are.
func (s *Store) ApplyWith(ctx context.Context, ev workflow.Event, obs workflow.Observed) (receipt workflow.Receipt, err error) {
	if s.readOnly {
		return workflow.Receipt{}, fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	if err := validateEvent(ev); err != nil {
		return workflow.Receipt{}, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return workflow.Receipt{}, mapBusy(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return workflow.Receipt{}, mapBusy(err)
	}
	committed := false
	defer func() {
		if !committed {
			// Not ctx: a cancelled context must not strand an open transaction.
			if _, rbErr := conn.ExecContext(context.Background(), `ROLLBACK`); rbErr != nil {
				err = errors.Join(err, rbErr)
			}
		}
	}()

	if dup, ok, err := s.duplicate(ctx, conn, ev); err != nil {
		return workflow.Receipt{}, err
	} else if ok {
		return dup, nil
	}

	var tr workflow.Transition
	var applied int64
	var attempt *workflow.Attempt
	if ev.AttemptID != "" {
		a, err := loadAttempt(ctx, conn, ev)
		if err != nil {
			return workflow.Receipt{}, err
		}
		attempt = &a
	}
	runVersion, runState, err := loadRun(ctx, conn, ev.RunID)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if runState == "finished" || runState == "archived" {
		return workflow.Receipt{}, fmt.Errorf("%w: run %s is %s", workflow.ErrInvalidTransition, ev.RunID, runState)
	}
	var cur workflow.Attempt
	if attempt != nil {
		cur = *attempt
		if !cur.Current {
			return workflow.Receipt{}, fmt.Errorf("%w: attempt %s was superseded", workflow.ErrStaleAttempt, cur.ID)
		}
		if ev.ExpectedVersion != cur.Version {
			return workflow.Receipt{}, fmt.Errorf("%w: attempt %s is at version %d, the event expected %d", workflow.ErrConflict, cur.ID, cur.Version, ev.ExpectedVersion)
		}
	} else {
		// Only a run-level stop has no attempt; it is versioned by the run.
		if ev.Kind != workflow.EventStopRequested {
			return workflow.Receipt{}, fmt.Errorf("%w: %s needs an attempt", workflow.ErrInvalidTransition, ev.Kind)
		}
		if ev.ExpectedVersion != runVersion {
			return workflow.Receipt{}, fmt.Errorf("%w: run %s is at version %d, the event expected %d", workflow.ErrConflict, ev.RunID, runVersion, ev.ExpectedVersion)
		}
		cur = workflow.Attempt{RunID: ev.RunID, Current: true}
	}

	facts, err := s.loadFacts(ctx, conn, cur, ev, obs)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if tr, err = workflow.Reduce(cur, ev, facts); err != nil {
		return workflow.Receipt{}, err
	}
	applied = tr.Attempt.Version
	if attempt == nil {
		applied = runVersion + 1
	}
	now := s.now().UnixMilli()

	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE hash=?`, ev.PayloadHash).Scan(&exists); err != nil {
		return workflow.Receipt{}, err
	}
	if exists == 0 {
		return workflow.Receipt{}, fmt.Errorf("%w: event payload artifact %s is not stored", workflow.ErrIncompleteEvidence, ev.PayloadHash)
	}

	// The event first: effects reference it.
	var attemptArg any
	if ev.AttemptID != "" {
		attemptArg = ev.AttemptID
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO events(id,run_id,attempt_id,kind,payload_hash,expected_version,applied_version,created_ms)
		VALUES(?,?,?,?,?,?,?,?)`, ev.ID, ev.RunID, attemptArg, ev.Kind, ev.PayloadHash, ev.ExpectedVersion, applied, now); err != nil {
		return workflow.Receipt{}, mapBusy(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('event',?,'payload',?)`, ev.ID, ev.PayloadHash); err != nil {
		return workflow.Receipt{}, err
	}
	if s.hooks.afterEvent != nil {
		if err := s.hooks.afterEvent(); err != nil {
			return workflow.Receipt{}, err
		}
	}

	if attempt != nil {
		if err := updateAttempt(ctx, conn, cur, tr.Attempt); err != nil {
			return workflow.Receipt{}, err
		}
		if tr.Accepted {
			res, err := conn.ExecContext(ctx, `UPDATE tasks SET accepted_attempt_id=? WHERE run_id=? AND id=? AND accepted_attempt_id IS NULL`,
				cur.ID, cur.RunID, cur.TaskID)
			if err != nil {
				return workflow.Receipt{}, err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return workflow.Receipt{}, fmt.Errorf("%w: task %s already has an accepted attempt", workflow.ErrConflict, cur.TaskID)
			}
		}
		// An accept or a repair settles the attempt, and with it its builder job.
		if err := settleBuilderJobs(ctx, conn, ev.RunID, now, false); err != nil {
			return workflow.Receipt{}, err
		}
	} else if _, err := conn.ExecContext(ctx, `UPDATE runs SET version=? WHERE id=? AND version=?`, applied, ev.RunID, runVersion); err != nil {
		return workflow.Receipt{}, err
	}
	if tr.PauseCode != "" {
		state := "paused"
		if tr.PauseCode == workflow.PauseStopping {
			state = "stopping"
		}
		if _, err := conn.ExecContext(ctx, `UPDATE runs SET
			pause_generation=pause_generation+CASE WHEN state<>'stopping' AND ?='paused' AND (state<>'paused' OR COALESCE(pause_code,'')<>?) THEN 1 ELSE 0 END,
			stop_generation=stop_generation+CASE WHEN ?='stopping' AND state<>'stopping' THEN 1 ELSE 0 END,
			pause_job_id=NULL, pause_effect_id=NULL,
			state=CASE WHEN state='stopping' THEN 'stopping' ELSE ? END, pause_code=? WHERE id=?`,
			state, tr.PauseCode, state, state, tr.PauseCode, ev.RunID); err != nil {
			return workflow.Receipt{}, err
		}
	}
	if s.hooks.afterAttempt != nil {
		if err := s.hooks.afterAttempt(); err != nil {
			return workflow.Receipt{}, err
		}
	}

	for i, intent := range tr.Effects {
		payload := intent.InputHash
		if payload == "" {
			payload = ev.PayloadHash
		}
		id := effectID(ev.ID, i)
		if _, err := conn.ExecContext(ctx, `INSERT INTO effects(id,run_id,attempt_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms)
			VALUES(?,?,?,?,?,?,?,?,'pending',0,?,?)`,
			id, ev.RunID, attemptArg, ev.ID, intent.Kind, i, payload, effectNonce(ev.ID, i), now, now); err != nil {
			return workflow.Receipt{}, fmt.Errorf("inserting effect %d (%s): %w", i, intent.Kind, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('effect',?,'payload',?)`, id, payload); err != nil {
			return workflow.Receipt{}, err
		}
	}
	if s.hooks.afterEffects != nil {
		if err := s.hooks.afterEffects(); err != nil {
			return workflow.Receipt{}, err
		}
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return workflow.Receipt{}, mapBusy(err)
	}
	committed = true
	return workflow.Receipt{EventID: ev.ID, Version: applied}, nil
}

// DerivedEffectIdentity is the id and nonce a derived effect of the given kind
// has, or will have when it is inserted: the existing one if the parent's
// event already has one, and the next in order if not. A prompt that must name
// its own effect, so a reply can echo it back, is written with this before the
// effect exists.
func (s *Store) DerivedEffectIdentity(ctx context.Context, parentID, kind string) (id, nonce string, err error) {
	var source string
	if err := s.db.QueryRowContext(ctx, `SELECT source_event_id FROM effects WHERE id=?`, parentID).Scan(&source); err != nil {
		return "", "", fmt.Errorf("%w: effect %s: %v", workflow.ErrConflict, parentID, err)
	}
	switch e := s.db.QueryRowContext(ctx, `SELECT id,nonce FROM effects WHERE source_event_id=? AND kind=? AND id<>?`, source, kind, parentID).Scan(&id, &nonce); {
	case e == nil:
		return id, nonce, nil
	case !errors.Is(e, sql.ErrNoRows):
		return "", "", e
	}
	var ordinal int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),-1)+1 FROM effects WHERE source_event_id=?`, source).Scan(&ordinal); err != nil {
		return "", "", err
	}
	return effectID(source, ordinal), effectNonce(source, ordinal), nil
}
