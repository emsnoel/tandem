package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// OutcomeKind says what one Tick did.
type OutcomeKind string

const (
	// Idle: nothing was claimable.
	Idle OutcomeKind = "idle"
	// Sent: the transport accepted the prompt. It is not yet acknowledged.
	Sent OutcomeKind = "sent"
	// NotSentRetry: a definite not-sent failure; the effect will be retried
	// after its backoff with the same id.
	NotSentRetry OutcomeKind = "not_sent_retry"
	// Failed: the effect has failed its last permitted send.
	Failed OutcomeKind = "failed"
	// Unknown: the send may have happened; the effect is delivery_unknown.
	Unknown OutcomeKind = "unknown"
	// Acknowledged: reconciliation saw this effect's nonce.
	Acknowledged OutcomeKind = "acknowledged"
	// StillUnknown: reconciliation learned nothing; operator action may be needed.
	StillUnknown OutcomeKind = "still_unknown"
	// Held: BeforeSend held the launch; the effect is pending again and no send
	// was attempted.
	Held OutcomeKind = "held"
)

// Outcome is the result of one Tick.
type Outcome struct {
	Kind     OutcomeKind
	EffectID string
}

// Deliverer sends the controller's effects. It does no I/O inside a
// transaction: it claims (committing the claim first), talks to the transport,
// then records the result under the fence it was given.
type Deliverer struct {
	Store     *state.Store
	Transport Transport
	Artifacts *artifact.Store
	// Owner names this worker in leases.
	Owner string
	// RunID is the run whose effects this deliverer claims. A tick drives one
	// run, and another run's leftover effect is never its to send or settle.
	RunID     string
	AgentKind string
	// Kinds restricts which effects this worker claims, so it never takes the
	// controller's local steps. Empty means every kind.
	Kinds []string
	// Target names the agent kind and session an effect goes to, when that
	// depends on the effect (a prompt for the builder, a notice for the
	// planner). Nil sends everything to AgentKind with no session.
	Target func(workflow.Effect) (agentKind, sessionID string)
	// After schedules the lease heartbeat; time.After when nil. Tests inject it.
	After func(time.Duration) <-chan time.Time
	// BeforeSend runs after the claim and before the transport is called, with
	// the delivery about to be sent. An error sends nothing. One wrapping
	// ErrHold returns the effect to pending untouched (outcome Held); any other
	// is recorded as definitely not sent, with its usual bounded retries. Nil
	// does nothing.
	BeforeSend func(ctx context.Context, eff workflow.Effect, d Delivery) error

	hooks hooks
}

// hooks are test failpoints. A hook error stops Tick at once with no further
// writes, as a killed process would.
type hooks struct {
	beforeSend     func() error
	afterSend      func() error
	afterHeartbeat func(error)
}

// storeError keeps why an effect's send failed as evidence, and returns its
// hash, or "" when it cannot be stored: a missing reason never stops the
// outcome being recorded.
func (d *Deliverer) storeError(ctx context.Context, id string, why error) string {
	hash, err := d.store(ctx, struct {
		EffectID string `json:"effect_id"`
		Error    string `json:"error"`
	}{id, why.Error()})
	if err != nil {
		return ""
	}
	return hash
}

// notSent records a definite not-sent outcome for a claimed effect, with why:
// the run that pauses for an effect that failed for good says so. A
// reason that cannot be stored does not stop the outcome being recorded.
func (d *Deliverer) notSent(ctx context.Context, id string, fence int64, why error) (Outcome, error) {
	// The cause, without NotSent's "definitely not sent:" prefix, which is the
	// same for every reason and only tells a reader what the outcome says.
	var ns *notSent
	if errors.As(why, &ns) {
		why = ns.err
	}
	if err := d.Store.RecordNotSent(ctx, id, fence, d.storeError(ctx, id, why)); err != nil {
		return Outcome{EffectID: id}, err
	}
	eff, err := d.Store.GetEffect(ctx, id)
	if err != nil {
		return Outcome{EffectID: id}, err
	}
	if eff.State == "failed" {
		return Outcome{Kind: Failed, EffectID: id}, nil
	}
	return Outcome{Kind: NotSentRetry, EffectID: id}, nil
}

// Tick claims at most one effect and carries it one step. It returns Idle when
// there is nothing to do. The caller loops; no model turn is spent polling.
func (d *Deliverer) Tick(ctx context.Context) (Outcome, error) {
	var eff workflow.Effect
	var err error
	if len(d.Kinds) > 0 {
		eff, err = d.Store.ClaimEffectKinds(ctx, d.Owner, d.RunID, d.Kinds)
	} else {
		eff, err = d.Store.ClaimEffect(ctx, d.Owner, d.RunID)
	}
	if errors.Is(err, state.ErrNoEffect) {
		return Outcome{Kind: Idle}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if eff.State == "delivery_unknown" {
		return d.reconcile(ctx, eff.ID, eff.Fence, eff.Nonce)
	}
	return d.send(ctx, eff)
}

func (d *Deliverer) send(ctx context.Context, eff workflow.Effect) (Outcome, error) {
	id, fence, nonce := eff.ID, eff.Fence, eff.Nonce
	kind, session := d.AgentKind, ""
	if d.Target != nil {
		kind, session = d.Target(eff)
	}
	if d.hooks.beforeSend != nil {
		if err := d.hooks.beforeSend(); err != nil {
			return Outcome{}, err
		}
	}
	delivery := Delivery{Kind: eff.Kind, EffectID: id, AttemptID: eff.AttemptID, Nonce: nonce, PromptHash: eff.PayloadHash, AgentKind: kind, SessionID: session}
	if d.BeforeSend != nil {
		if err := d.BeforeSend(ctx, eff, delivery); errors.Is(err, ErrHold) {
			if rerr := d.Store.ReleaseClaim(ctx, id, fence); rerr != nil {
				return Outcome{EffectID: id}, errors.Join(err, rerr)
			}
			return Outcome{Kind: Held, EffectID: id}, err
		} else if err != nil {
			out, rerr := d.notSent(ctx, id, fence, err)
			return out, errors.Join(err, rerr)
		}
	}
	stop := d.heartbeat(ctx, id, fence)
	receipt, sendErr := d.Transport.Send(ctx, delivery)
	stop()

	// The agent may acknowledge with its nonce before the transport finishes
	// confirming the send. The acknowledgement proves delivery, so the effect is
	// sent and never retried. A receipt that came back is still stored.
	if sendErr != nil {
		if cur, err := d.Store.GetEffect(ctx, id); err == nil && cur.State == "acknowledged" {
			return Outcome{Kind: Sent, EffectID: id}, nil
		}
	}

	switch {
	case sendErr == nil:
		if d.hooks.afterSend != nil {
			if err := d.hooks.afterSend(); err != nil {
				return Outcome{}, err
			}
		}
		hash, err := d.store(ctx, struct {
			EffectID     string `json:"effect_id"`
			Nonce        string `json:"nonce"`
			ExternalID   string `json:"external_id"`
			EvidenceHash string `json:"evidence_hash,omitempty"`
		}{id, nonce, receipt.ExternalID, receipt.EvidenceHash})
		if err != nil {
			// The send happened; failing to write the receipt must not read as
			// not-sent. The lease expires into delivery_unknown.
			return Outcome{EffectID: id}, fmt.Errorf("the send of %s succeeded but its receipt could not be stored: %w", id, err)
		}
		if err := d.Store.RecordDelivery(ctx, id, fence, hash); err != nil {
			return Outcome{EffectID: id}, err
		}
		return Outcome{Kind: Sent, EffectID: id}, nil

	case IsNotSent(sendErr):
		return d.notSent(ctx, id, fence, sendErr)

	default:
		// Not provably unsent, so it may have been sent. Keep the evidence and
		// do not retry.
		if err := d.Store.MarkDeliveryUnknown(ctx, id, fence, d.storeError(ctx, id, sendErr)); err != nil {
			return Outcome{EffectID: id}, err
		}
		return Outcome{Kind: Unknown, EffectID: id}, nil
	}
}

// reconcile asks the transport what became of an effect whose send may have
// happened. It never sends. Only the effect's own nonce, seen on a live agent,
// acknowledges it.
func (d *Deliverer) reconcile(ctx context.Context, id string, fence int64, nonce string) (Outcome, error) {
	obs, err := d.Transport.Observe(ctx, id)
	if err != nil {
		_ = d.Store.ReleaseReconcile(ctx, id, fence)
		return Outcome{Kind: StillUnknown, EffectID: id}, fmt.Errorf("observing effect %s: %w", id, err)
	}
	if obs.Nonce == nonce && nonce != "" && (obs.State == ObsRunning || obs.State == ObsCompleted) {
		if err := d.Store.AcknowledgeEffect(ctx, id, nonce); err != nil {
			return Outcome{EffectID: id}, err
		}
		return Outcome{Kind: Acknowledged, EffectID: id}, nil
	}
	if err := d.Store.ReleaseReconcile(ctx, id, fence); err != nil {
		return Outcome{EffectID: id}, err
	}
	return Outcome{Kind: StillUnknown, EffectID: id}, nil
}

// heartbeat renews the lease every HeartbeatInterval until the returned stop
// function is called, which waits for the loop to end so a late heartbeat can
// never race the write that follows the send.
func (d *Deliverer) heartbeat(ctx context.Context, id string, fence int64) (stop func()) {
	after := d.After
	if after == nil {
		after = time.After
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-after(state.HeartbeatInterval):
				err := d.Store.HeartbeatEffect(ctx, id, fence)
				if d.hooks.afterHeartbeat != nil {
					d.hooks.afterHeartbeat(err)
				}
				if err != nil {
					return // the lease is lost; the final write will be refused
				}
			}
		}
	}()
	return func() { close(done); <-finished }
}

func (d *Deliverer) store(ctx context.Context, v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	hash, err := d.Artifacts.Put(ctx, &buf)
	if err != nil {
		return "", err
	}
	return hash, d.Store.AdoptArtifact(ctx, hash)
}
