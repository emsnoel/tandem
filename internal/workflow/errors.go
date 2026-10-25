// Package workflow holds the durable runner's pure records and errors. It
// imports nothing from the rest of tandem so storage, controller and runner
// can all depend on it without cycles.
package workflow

import "errors"

// Domain errors. Wrap with %w and test with errors.Is. Database-specific
// errors, such as a newer schema, stay in internal/state.
var (
	// ErrConflict: a compare-and-set lost, or the same identity was reused
	// with different content.
	ErrConflict = errors.New("conflicting update")
	// ErrStaleAttempt: an event names an attempt that is not the current one.
	ErrStaleAttempt = errors.New("stale attempt")
	// ErrInvalidTransition: the state machine does not allow this event here.
	ErrInvalidTransition = errors.New("invalid transition")
	// ErrCorruptArtifact: stored bytes do not match their hash.
	ErrCorruptArtifact = errors.New("corrupt artifact")
	// ErrIncompleteEvidence: required evidence is missing or partial.
	ErrIncompleteEvidence = errors.New("incomplete evidence")
	// ErrDeliveryUnknown: a send may or may not have happened.
	ErrDeliveryUnknown = errors.New("delivery unknown")
	// ErrContextTooLarge: mandatory task context exceeds its limit.
	ErrContextTooLarge = errors.New("mandatory context too large")
	// ErrMemoryConflict: required memory entries contradict each other.
	ErrMemoryConflict = errors.New("conflicting required memory")
	// ErrBusy: another operation holds the lock this one needs.
	ErrBusy = errors.New("busy")
	// ErrUnsupported: the requested capability is not available.
	ErrUnsupported = errors.New("unsupported")
)
