package controller

// Mailbox v2: how a builder reports to a durable run.
//
// The builder writes a payload under payloads/ and an envelope into q/ of
// <relay>/durable/<run-id>/. The bridge claims the envelope by renaming it and
// hands it to ProcessClaimed, which validates it, stores the payload as an
// artifact, applies the event, writes a receipt, and only then removes the
// file. A message that can never apply is quarantined with a reason. Builders
// may send only allowlisted kinds and cannot name a check result, version,
// acceptance or budget.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// MaxEnvelopeBytes and MaxPayloadBytes bound what a builder can make the
	// controller read.
	MaxEnvelopeBytes = 16 * 1024
	MaxPayloadBytes  = 1 << 20
	// ActiveRunFile marks a relay as carrying a durable run. Updated entrypoints
	// look for it before they look for a legacy build.json.
	ActiveRunFile = "active-run.json"

	maxPayloadPath = 256
)

var (
	taskPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	pathPattern = regexp.MustCompile(`^payloads(/[A-Za-z0-9._-]+)+$`)
)

// builderKinds are the kinds a builder's mailbox may carry. Of these, only the
// first three are applied as events by this version; an artifact_request is
// answered without an event.
var builderKinds = map[string]bool{
	workflow.EventBuilderStarted: true, workflow.EventBuilderSubmitted: true, workflow.EventBuilderStuck: true,
	"review_result": true, "artifact_request": true,
}

// Review protocol 3 adds two kinds a reviewer may send; builders keep speaking
// version 2. The version of a message says who it is from, and the run's pinned
// review protocol says whether it may speak at all.
const (
	EventReviewStarted = "review_started"
	EventReviewResult  = "review_result"
)

var reviewerKinds = map[string]bool{EventReviewStarted: true, EventReviewResult: true}

// IsBuilderKind reports whether a builder's mailbox may carry kind, and
// IsReviewerKind whether a reviewer may send it: the one list `tandem build
// event new` and ingestion both check.
func IsBuilderKind(kind string) bool  { return builderKinds[kind] }
func IsReviewerKind(kind string) bool { return reviewerKinds[kind] }

// Envelope is the wire message. Every field is required. Version 2 is the
// builder's; version 3 is a reviewer's.
type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	RunID         string `json:"run_id"`
	TaskID        string `json:"task_id"`
	AttemptID     string `json:"attempt_id"`
	JobID         string `json:"job_id"`
	EffectID      string `json:"effect_id"`
	Nonce         string `json:"nonce"`
	Kind          string `json:"kind"`
	PayloadPath   string `json:"payload_path"`
	PayloadHash   string `json:"payload_hash"`
}

// RejectError is a message that can never be applied, with a stable machine
// code. Wrapped is the workflow error it corresponds to, when there is one.
type RejectError struct {
	Code string
	Msg  string
	Err  error
}

func (e *RejectError) Error() string { return e.Code + ": " + e.Msg }
func (e *RejectError) Unwrap() error { return e.Err }

func reject(code, format string, a ...any) *RejectError {
	return &RejectError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

func rejectWith(code string, err error, format string, a ...any) *RejectError {
	return &RejectError{Code: code, Msg: fmt.Sprintf(format, a...), Err: err}
}

// ErrBuilderWorking is a submission from a builder whose turn has not ended.
// A builder hands back with a command it runs inside its turn, and may write
// a closing message after it, so this is ordinary: the message waits in the
// queue and is applied once the turn ends. It is never a rejection.
var ErrBuilderWorking = errors.New("the builder is still working; its submission is applied once its turn ends")

// MailboxDeps are what ingestion needs. Dir is this run's directory and RunID
// the only run it accepts.
type MailboxDeps struct {
	State     *state.Store
	Artifacts *artifact.Store
	Dir       string
	RunID     string
	// Observed supplies the facts only the controller can establish, such as
	// whether the builder's session has settled. Nil means none: a submission
	// is then refused rather than assumed settled.
	Observed func(ctx context.Context, env Envelope) workflow.Observed

	// StillWorking reports whether the builder that sent env is still in its
	// turn. A submission sent mid-turn is then left for a retry, before its
	// evidence is captured, instead of being refused for good.
	StillWorking func(ctx context.Context, env Envelope) bool

	// Prepare runs after a message is validated and its payload imported, and
	// before the event is applied. The controller uses it to capture the end
	// snapshot and patch a submission needs. An error leaves the message for a
	// retry unless it is a *RejectError.
	Prepare func(ctx context.Context, env Envelope, attempt workflow.Attempt) error

	// ReviewProtocol is the run's pinned review protocol. Below 3, version 3
	// messages are refused. ReviewDeadline is how long a review that has
	// started may run; ObserveSession is the session the transport saw for an
	// effect, when it saw one.
	ReviewRejected func(context.Context, Envelope, string) error
	ReviewProtocol int
	ReviewDeadline time.Duration
	ObserveSession func(ctx context.Context, effectID string) string

	// Test failpoints.
	beforeOpen   func()
	afterReceipt func() error
}

// ParseEnvelope decodes and validates the shape of an envelope, with no access
// to any state.
func ParseEnvelope(raw []byte) (Envelope, error) {
	var env Envelope
	if len(raw) > MaxEnvelopeBytes {
		return env, reject("malformed_envelope", "the envelope is %d bytes, over %d", len(raw), MaxEnvelopeBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return Envelope{}, reject("malformed_envelope", "not a v2 envelope: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Envelope{}, reject("malformed_envelope", "data after the envelope")
	}
	if env.SchemaVersion != 2 && env.SchemaVersion != 3 {
		return Envelope{}, reject("unsupported_protocol", "schema_version %d; this run speaks 2 and, if it opted in, 3", env.SchemaVersion)
	}
	for name, v := range map[string]string{"event_id": env.EventID, "run_id": env.RunID, "attempt_id": env.AttemptID,
		"job_id": env.JobID, "effect_id": env.EffectID, "nonce": env.Nonce} {
		if !idPattern.MatchString(v) {
			return Envelope{}, reject("invalid_id", "%s must be 32 lowercase hex characters", name)
		}
	}
	if !taskPattern.MatchString(env.TaskID) {
		return Envelope{}, reject("invalid_id", "task_id is not a task slug")
	}
	if len(env.PayloadHash) != 64 || strings.Trim(env.PayloadHash, "0123456789abcdef") != "" {
		return Envelope{}, reject("invalid_id", "payload_hash must be 64 lowercase hex characters")
	}
	// Whether a kind belongs to the version it arrived under is decided at
	// ingestion, after the run's protocol pin: a run that did not opt in says
	// unsupported_protocol for every version 3 message, whatever its kind.
	if !builderKinds[env.Kind] && !(env.SchemaVersion == 3 && reviewerKinds[env.Kind]) {
		for _, k := range workflow.AllEventKinds {
			if k == env.Kind {
				return Envelope{}, reject("controller_only_event", "%s can only be issued by the controller", env.Kind)
			}
		}
		return Envelope{}, reject("unsupported_kind", "unknown kind %q", env.Kind)
	}
	if err := validPayloadPath(env.PayloadPath); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

func validPayloadPath(p string) error {
	if len(p) == 0 || len(p) > maxPayloadPath || strings.ContainsRune(p, 0) || !pathPattern.MatchString(p) {
		return reject("invalid_payload_path", "payload_path must be relative, under payloads/, with plain components")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return reject("invalid_payload_path", "payload_path may not contain . or ..")
		}
	}
	return nil
}

// IngestMailbox validates one envelope, imports its payload as an artifact and
// applies its event. The returned receipt is the event's: a replay returns the
// original with Duplicate set. Errors that are a *RejectError, or wrap a
// workflow error, are permanent for that message; anything else is transient.
func IngestMailbox(ctx context.Context, deps MailboxDeps, raw []byte) (workflow.Receipt, error) {
	env, err := ParseEnvelope(raw)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if env.RunID != deps.RunID {
		return workflow.Receipt{}, reject("wrong_run", "the message is for run %s; this mailbox serves %s", env.RunID, deps.RunID)
	}
	if env.SchemaVersion == 3 {
		return ingestReviewer(ctx, deps, env)
	}
	if env.Kind == "review_result" {
		return workflow.Receipt{}, rejectWith("unsupported_kind", workflow.ErrUnsupported, "%s is not applied by this version", env.Kind)
	}

	attempt, err := currentAttempt(ctx, deps, env)
	if err != nil {
		return workflow.Receipt{}, err
	}

	eff, err := deps.State.GetEffect(ctx, env.EffectID)
	switch {
	case errors.Is(err, workflow.ErrConflict):
		return workflow.Receipt{}, reject("unknown_effect", "effect %s does not exist", env.EffectID)
	case err != nil:
		return workflow.Receipt{}, err
	case eff.AttemptID != attempt.ID || eff.RunID != env.RunID || eff.Kind != workflow.EffectBuilderDispatch:
		return workflow.Receipt{}, reject("unknown_effect", "effect %s is not this attempt's builder dispatch", env.EffectID)
	case eff.Nonce != env.Nonce:
		return workflow.Receipt{}, reject("wrong_nonce", "the nonce does not belong to effect %s", env.EffectID)
	}

	job, found, err := deps.State.GetJob(ctx, env.JobID)
	switch {
	case err != nil:
		return workflow.Receipt{}, err
	case found && (job.AttemptID != attempt.ID || job.Kind != "builder"):
		return workflow.Receipt{}, reject("unknown_job", "job %s is not this attempt's builder job", env.JobID)
	case !found && env.Kind != workflow.EventBuilderStarted:
		// The start acknowledgement can precede the job's bookkeeping; nothing
		// the builder reports afterwards can.
		return workflow.Receipt{}, reject("unknown_job", "job %s does not exist", env.JobID)
	}

	payload, err := importPayload(ctx, deps, env)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if env.Kind == "artifact_request" {
		return serveArtifactRequest(ctx, deps, env, attempt, payload)
	}
	if env.Kind == workflow.EventBuilderSubmitted && deps.StillWorking != nil && deps.StillWorking(ctx, env) {
		return workflow.Receipt{}, ErrBuilderWorking
	}
	if deps.Prepare != nil {
		if err := deps.Prepare(ctx, env, attempt); err != nil {
			return workflow.Receipt{}, err
		}
	}
	// The nonce is valid for this effect, so the prompt was read.
	if err := deps.State.AcknowledgeEffect(ctx, eff.ID, env.Nonce); err != nil {
		return workflow.Receipt{}, err
	}
	var obs workflow.Observed
	if deps.Observed != nil {
		obs = deps.Observed(ctx, env)
	}
	// The controller, not the builder, picks the expected version; a version
	// that moved under it is re-read a few times.
	for try := 0; ; try++ {
		cur, err := deps.State.GetAttempt(ctx, env.AttemptID)
		if err != nil {
			return workflow.Receipt{}, err
		}
		rec, err := deps.State.ApplyWith(ctx, workflow.Event{ID: env.EventID, RunID: env.RunID, AttemptID: env.AttemptID,
			Kind: env.Kind, PayloadHash: payload, ExpectedVersion: cur.Version}, obs)
		if errors.Is(err, workflow.ErrConflict) && try < 2 {
			continue
		}
		if errors.Is(err, workflow.ErrConflict) {
			return workflow.Receipt{}, rejectWith("conflict", err, "%v", err)
		}
		return rec, err
	}
}
