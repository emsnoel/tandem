// Package runner delivers the controller's effects to agents. A transport sends
// a prompt and reports whether it was sent, certainly not sent, or may have
// been sent; a loop claims one effect at a time under a fenced lease and never
// resends after an ambiguous send.
//
// A receipt means the transport accepted the request, not that the agent read
// it. Only the effect's nonce, returned in the agent's first action, confirms a
// prompt.
package runner

import (
	"context"
	"errors"
	"fmt"
)

// Delivery is one send of an effect's prompt. The same effect keeps the same
// EffectID and Nonce across every retry.
type Delivery struct {
	// Kind is the effect's kind (builder_dispatch, review_prompt, notify_planner,
	// cancel). A transport behaves differently for each: only a task prompt
	// starts a fresh conversation, and only a cancel may reach a working agent.
	Kind      string
	EffectID  string
	AttemptID string
	// Nonce travels in the prompt; the agent returns it to acknowledge.
	Nonce      string
	PromptHash string
	AgentKind  string
	// SessionID is the agent to send to: the name the run was started with
	// for its builder or planner.
	SessionID string
}

// Receipt says the transport accepted the request. ExternalID is the
// transport's own handle (a session, a pane, a process); EvidenceHash may name
// an artifact holding what the transport returned.
type Receipt struct {
	EffectID     string
	ExternalID   string
	EvidenceHash string
}

// Observation states.
const (
	ObsUnknown   = "unknown"
	ObsRunning   = "running"
	ObsCompleted = "completed"
	ObsStopped   = "stopped"
)

// Observation is what the transport can see of an effect's agent. Nonce is the
// acknowledged nonce, if any. State unknown means the transport could not
// tell, which differs from stopped. ExternalID names the agent by session, else
// by pane. Session is only the conversation id the CLI reported.
type Observation struct {
	State      string
	Nonce      string
	ExternalID string
	Session    string
}

// Transport sends prompts and observes agents. Implementations exist per
// adapter; none may report ErrNotSent unless nothing can have been delivered.
type Transport interface {
	// Send delivers d. A nil error is a receipt. An error wrapping ErrNotSent
	// means nothing was delivered and the effect may be retried. Any other
	// error is uncertain: the prompt may have arrived.
	Send(ctx context.Context, d Delivery) (Receipt, error)
	// Observe reports on the agent that effectID was sent to.
	Observe(ctx context.Context, effectID string) (Observation, error)
}

// AgentObserver is implemented by a transport that can observe an agent by the
// name an effect was sent to. Observe takes only an effect id, and a transport
// that tracks one agent can answer only for that agent. A prompt sent to
// another agent, the planner's verification say, is observed by name instead.
type AgentObserver interface {
	ObserveAgent(ctx context.Context, name string) (Observation, error)
}

// PaneReader is implemented by a transport that can read the end of what an
// agent's pane shows, unwrapped. The controller reads a pane review's reply
// from it.
type PaneReader interface {
	ReadAgent(ctx context.Context, name string, lines int) (string, error)
}

// ErrAgentGone is what a transport wraps when the host reports that the agent
// itself is gone (Herdr's agent_not_found), as opposed to not answering.
var ErrAgentGone = errors.New("the agent is gone")

// ErrHold marks a BeforeSend refusal that is not a failure: the launch may not
// be sent now (its collaboration is stopped or out of budget). The effect goes
// back to pending with its retries untouched, to be sent once released.
var ErrHold = errors.New("launch held")

// Hold marks err as a hold.
func Hold(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrHold, err)
}

// ErrNotSent marks a failure that certainly delivered nothing: the socket
// refused the connection, the pane did not exist, the request was never
// written. It is the only error that permits a retry.
var ErrNotSent = errors.New("definitely not sent")

// NotSent marks err as a definite not-sent failure.
func NotSent(err error) error {
	if err == nil {
		return nil
	}
	return &notSent{err}
}

type notSent struct{ err error }

func (e *notSent) Error() string   { return ErrNotSent.Error() + ": " + e.err.Error() }
func (e *notSent) Unwrap() []error { return []error{ErrNotSent, e.err} }

// IsNotSent reports whether err is a definite not-sent failure. Anything else,
// a cancelled context included, is treated as uncertain.
func IsNotSent(err error) bool { return errors.Is(err, ErrNotSent) }
