package controller

// The run lifecycle. A Controller owns one checkout's writer lease and
// carries a run through start, dispatch, acknowledgement, submission, check,
// review, acceptance and the final notice. Every step is an event or effect in
// the store, so Recover can continue from any crash without sending twice.
//
// The bridge drives it: Tick makes bounded progress and returns.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
)

// leaseTTL is how long the writer lease lasts between ticks. Each Tick renews
// it; one that is left to expire becomes reconciling, never a takeover.
const leaseTTL = 10 * time.Minute

// Effect kinds the controller itself defines beyond the workflow's.
const (
	effectReviewPrompt = "review_prompt"
	effectNotify       = "notify_planner"
)

// ErrNotWriter: this controller does not hold the checkout's writer lease.
// Call Recover, which reconciles a previous writer before taking over.
var ErrNotWriter = errors.New("this controller does not hold the checkout's writer lease")

// Dependencies are everything a Controller touches outside itself. Tests
// inject fakes for Transport and CheckExecutor and fixed Now and NewID; there
// are no package-level live dependencies.
type Dependencies struct {
	State         *state.Store
	Artifacts     *artifact.Store
	Transport     runner.Transport
	CheckExecutor func(context.Context, CheckRequest) (CheckResult, error)
	Now           func() time.Time
	NewID         func() (string, error)
	// Owner names this controller in writer leases. It must be stable across
	// the processes of one logical controller (a CLI that runs a tick at a
	// time) and distinct from any other. Empty means a fresh random owner,
	// which suits a long-lived process and tests.
	Owner string
	// Reviewer decides whether a review runs as a direct process. The zero value
	// never does: every review runs in a pane.
	Reviewer runner.Reviewer
	// ReviewLaunch resolves the launch arguments a direct review is given for a
	// builder kind. Nil resolves them from the builder's recorded autonomy.
	ReviewLaunch func(kind string) ([]string, error)
	// ReviewTimeout bounds one direct review; 5 minutes when zero.
	ReviewTimeout time.Duration
	// UsageSource finds the agents' logs for usage accounting. Nil means none can
	// be read, and usage is reported as unavailable.
	UsageSource UsageSource
	// SessionLogs finds the provider logs a collaboration's sources wrote. Nil
	// means none can be read: collection reports every source as missing.
	SessionLogs SessionLogs
}

// StartRequest begins a run.
type StartRequest struct {
	Root           string
	PlanPath       string
	BuilderKind    string
	BuilderSession string
	PlannerSession string
	CheckCommand   string
	TaskInputLimit int64
	RunInputLimit  int64
	// ContextLimit is the most bytes of prompt a builder is given for a task;
	// 0 means taskcontext.DefaultMaxEstimatedTokens.
	ContextLimit int
	// Collaboration, when set, attaches the run to an open collaboration: every
	// model job it launches is registered as an expected source first.
	Collaboration string
	// ReviewProtocol and ReviewDelivery opt the run into direct review handback:
	// protocol 3 with delivery "mailbox". Zero and "" mean the original manual
	// protocol 2, and a run never changes either after it starts.
	ReviewProtocol int
	ReviewDelivery string
	// AcceptancePolicyPath is the production acceptance policy a mailbox run
	// starts under: both review stages are mandatory. It is read once and its
	// hash pinned in the run's configuration.
	AcceptancePolicyPath string
	// RecoveryPolicyPath opts the run into bounded recovery (bounded-v1). It
	// needs a collaboration, whose budget groups hold the repair allowance.
	// Without it the run behaves as it did before recovery existed.
	RecoveryPolicyPath string
	// ReviewerKind and ReviewerSession name a reviewer pane: every review
	// prompt goes there instead of to the builder. Both or neither; the pane is
	// never the builder's or the planner's. Without one, reviews run where they
	// always did.
	ReviewerKind    string
	ReviewerSession string
	// ConventionsFile, PromptExtra and the ruling come from .tandem.json and
	// the handoff. Every task prompt of the run carries them; see
	// prompt_settings.go.
	ConventionsFile string
	PromptExtra     string
	Ruling          string
	RulingObjection string
	// ResumeFrom, when set, resumes an archived run of the same plan:
	// ResumeLatest for the most recent one, or a run id. See resume.go.
	ResumeFrom string
}

// runConfig is the run's immutable configuration, stored as an artifact named
// by the run's config hash.
type runConfig struct {
	Version        int    `json:"version"`
	Root           string `json:"root"`
	PlanPath       string `json:"plan_path"`
	BuilderKind    string `json:"builder_kind"`
	BuilderSession string `json:"builder_session"`
	PlannerKind    string `json:"planner_kind"`
	PlannerSession string `json:"planner_session"`
	CheckCommand   string `json:"check_command"`
	TaskInputLimit int64  `json:"task_input_limit"`
	RunInputLimit  int64  `json:"run_input_limit"`
	ContextLimit   int    `json:"context_limit,omitempty"`
	// Collaboration is omitted when empty, so a run without one has the same
	// configuration, and hash, as before collaborations existed.
	Collaboration string `json:"collaboration,omitempty"`
	// ReviewProtocol and ReviewDelivery are omitted at their defaults, so a run
	// that did not opt in has the same configuration, and hash, as before.
	ReviewProtocol int    `json:"review_protocol,omitempty"`
	ReviewDelivery string `json:"review_delivery,omitempty"`
	// AcceptancePolicy is the hash of the policy artifact the run started
	// under. Its presence makes independent verification a requirement of
	// acceptance for the whole life of the run.
	AcceptancePolicy string `json:"acceptance_policy,omitempty"`
	// RecoveryPolicy is the hash of the bounded-v1 policy the run started
	// under; its presence turns bounded recovery on for the whole life of the run.
	RecoveryPolicy string `json:"recovery_policy,omitempty"`
	// ReviewerKind and ReviewerSession are the reviewer pane, omitted when the
	// run has none so its configuration and hash are what they were before.
	ReviewerKind    string `json:"reviewer_kind,omitempty"`
	ReviewerSession string `json:"reviewer_session,omitempty"`
	// Settings is what the task prompts carry from the project and the
	// handoff, omitted when there is none so the hash is what it was before.
	Settings *promptSettings `json:"settings,omitempty"`
	// BuilderHandbackRelay pins the helper path across rendering and delivery.
	// Older runs kept their long-form handback in the prompt.
	BuilderHandbackRelay string `json:"builder_handback_relay,omitempty"`
	// ResumedFrom is the run this one resumed, omitted for a run that started
	// at the beginning so its hash is what it was before.
	ResumedFrom string `json:"resumed_from,omitempty"`
}

// relay is the run's relay directory, derived from its checkout root.
func (cfg runConfig) relay() string { return paths.Relay(cfg.Root) }

type hooks struct {
	// afterAccept fails Tick right after an acceptance commits.
	afterAccept func() error
	// afterAttemptCreated fails startAttempt right after the attempt and its
	// packet are persisted, before anything is scheduled to send.
	afterAttemptCreated func() error
	// afterReconcileAttempt runs once a reconcile attempt exists and before it
	// is submitted: the window a crash must be able to finish from.
	afterReconcileAttempt func() error
	// beforeExportComplete fails Export after every artifact is copied and
	// verified, before it publishes the completion marker.
	beforeExportComplete func() error
	// beforeDeliver fails a pass after its local steps, before it delivers.
	beforeDeliver func() error

	// The review application boundaries, so a test can place a stop, a pause
	// or a lease change exactly between two steps: after the fence is read,
	// after the reviewer is observed settled, before the guarded write and
	// after it.
	afterReviewFence     func() error
	afterReviewObserved  func() error
	beforeCandidateApply func() error
	afterCandidateApply  func() error
}

// Controller drives runs.
type Controller struct {
	d      Dependencies
	owner  string
	mu     sync.Mutex
	fences map[string]int64
	hooks  hooks
}

// New builds a Controller. Every dependency is required.
func New(d Dependencies) (*Controller, error) {
	switch {
	case d.State == nil || d.Artifacts == nil:
		return nil, errors.New("controller: a state store and an artifact store are required")
	case d.Transport == nil || d.CheckExecutor == nil:
		return nil, errors.New("controller: a transport and a check executor are required")
	}
	if d.NewID == nil {
		d.NewID = randomID
	}
	if d.Now != nil {
		d.State.WithClock(d.Now)
	} else {
		d.Now = time.Now
	}
	owner := d.Owner
	if owner == "" {
		var err error
		if owner, err = d.NewID(); err != nil {
			return nil, err
		}
	}
	return &Controller{d: d, owner: owner, fences: map[string]int64{}}, nil
}

func derive(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:16])
}

func (c *Controller) setFence(checkout string, fence int64) {
	c.mu.Lock()
	c.fences[checkout] = fence
	c.mu.Unlock()
}

func (c *Controller) fence(checkout string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fences[checkout]
}

// put stores bytes as an artifact and registers it for use in rows.
func (c *Controller) put(ctx context.Context, content string) (string, error) {
	h, err := c.d.Artifacts.Put(ctx, strings.NewReader(content))
	if err != nil {
		return "", err
	}
	return h, c.d.State.AdoptArtifact(ctx, h)
}

func (c *Controller) putJSON(ctx context.Context, v any) (string, error) {
	return c.put(ctx, string(encode(v)))
}

func (c *Controller) config(ctx context.Context, run state.Run) (runConfig, error) {
	raw, err := c.d.Artifacts.ReadAll(ctx, run.ConfigHash, 1<<20)
	if err != nil {
		return runConfig{}, err
	}
	var cfg runConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return runConfig{}, fmt.Errorf("the run's configuration is corrupt: %w", err)
	}
	return cfg, nil
}

// RunReviewProtocol is the review protocol a run was started with, read from
// its pinned configuration. Whatever the environment says now, a run keeps the
// protocol it began with: a run started before the opt-in stays on protocol 2.
func RunReviewProtocol(ctx context.Context, st *state.Store, art *artifact.Store, runID string) (int, error) {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	c := &Controller{d: Dependencies{State: st, Artifacts: art}}
	cfg, err := c.config(ctx, run)
	if err != nil {
		return 0, err
	}
	if cfg.ReviewProtocol < 2 {
		return 2, nil
	}
	return cfg.ReviewProtocol, nil
}
