package workflow

// State vocabularies. The strings are stored in SQLite CHECK constraints, so
// changing one is a schema migration.
type (
	ImplementationState string
	CheckState          string
	ReviewState         string
	BudgetState         string
)

const (
	ImplementationPending   ImplementationState = "pending"
	ImplementationRunning   ImplementationState = "running"
	ImplementationSubmitted ImplementationState = "submitted"
	ImplementationStuck     ImplementationState = "stuck"

	CheckPending       CheckState = "pending"
	CheckRunning       CheckState = "running"
	CheckPassed        CheckState = "passed"
	CheckFailed        CheckState = "failed"
	CheckNotApplicable CheckState = "not_applicable"
	CheckInvalid       CheckState = "invalid"

	ReviewPending          ReviewState = "pending"
	ReviewRunning          ReviewState = "running"
	ReviewPassed           ReviewState = "passed"
	ReviewChangesRequested ReviewState = "changes_requested"
	ReviewWaived           ReviewState = "waived"
	ReviewNotApplicable    ReviewState = "not_applicable"
	ReviewInvalid          ReviewState = "invalid"
	ReviewFailed           ReviewState = "failed"

	BudgetAvailable BudgetState = "available"
	BudgetWarning   BudgetState = "warning"
	BudgetExhausted BudgetState = "exhausted"
	BudgetUnknown   BudgetState = "unknown"
)

// Event kinds. They are defined here and nowhere else: a mailbox, a controller
// and the store all name events through these constants.
const (
	EventAttemptCreated   = "attempt_created"
	EventBuilderStarted   = "builder_started"
	EventBuilderSubmitted = "builder_submitted"
	EventBuilderStuck     = "builder_stuck"
	EventCheckCompleted   = "check_completed"
	EventReviewCompleted  = "review_completed"
	EventRepairStarted    = "repair_started"
	EventBudgetChanged    = "budget_changed"
	EventAcceptRequested  = "accept_requested"
	EventWaiveRequested   = "waive_requested"
	EventStopRequested    = "stop_requested"
)

// AllEventKinds lists every declared event, in the order of the C3 table.
var AllEventKinds = []string{
	EventAttemptCreated, EventBuilderStarted, EventBuilderSubmitted, EventBuilderStuck,
	EventCheckCompleted, EventReviewCompleted, EventRepairStarted, EventBudgetChanged,
	EventAcceptRequested, EventWaiveRequested, EventStopRequested,
}

// BuilderEventKinds are the only events a builder's mailbox may submit. Check
// completion, acceptance, waivers and budget changes are controller-only.
var BuilderEventKinds = []string{EventBuilderStarted, EventBuilderSubmitted, EventBuilderStuck}

// Effect kinds a transition can ask the controller to carry out.
const (
	EffectBuilderDispatch     = "builder_dispatch"
	EffectCheckDispatch       = "check_dispatch"
	EffectReviewDispatch      = "review_dispatch"
	EffectCreateRepairAttempt = "create_repair_attempt"
	EffectAdvanceRun          = "advance_run"
	EffectCancel              = "cancel"
)

// Pause codes. A transition that sets one pauses the run (PauseStopping moves
// it to stopping).
const (
	PauseStuck        = "stuck"
	PauseCheckFailed  = "check_failed"
	PauseReviewFailed = "review_failed"
	PauseScheduling   = "scheduling_blocked"
	PauseStopping     = "stopping"
)

// TaskSpec is one task of a plan.
type TaskSpec struct {
	ID          string   `json:"id"`
	Brief       string   `json:"brief"`
	Constraints string   `json:"constraints"`
	DependsOn   []string `json:"depends_on"`
	Paths       []string `json:"paths"`
	Outputs     []string `json:"outputs"`
}

// Attempt is one try at a task. A repair is a new attempt linked to its parent.
type Attempt struct {
	ID                string              `json:"id"`
	RunID             string              `json:"run_id"`
	TaskID            string              `json:"task_id"`
	ParentAttemptID   string              `json:"parent_attempt_id,omitempty"`
	Ordinal           int                 `json:"ordinal"`
	Implementation    ImplementationState `json:"implementation_state"`
	Check             CheckState          `json:"check_state"`
	Review            ReviewState         `json:"review_state"`
	Budget            BudgetState         `json:"budget_state"`
	StartSnapshotHash string              `json:"start_snapshot_hash,omitempty"`
	EndSnapshotHash   string              `json:"end_snapshot_hash,omitempty"`
	PatchHash         string              `json:"patch_hash,omitempty"`
	PacketHash        string              `json:"packet_hash,omitempty"`
	Version           int64               `json:"version"`
	Current           bool                `json:"current"`
	AcceptedMS        *int64              `json:"accepted_ms"`
}

// Event is one identified fact about an attempt (or, for stop_requested, a run).
type Event struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	AttemptID       string `json:"attempt_id,omitempty"`
	Kind            string `json:"kind"`
	PayloadHash     string `json:"payload_hash"`
	ExpectedVersion int64  `json:"expected_version"`
}

// Receipt is what applying an event returns. A duplicate carries the version
// the original application produced.
type Receipt struct {
	EventID   string `json:"event_id"`
	Version   int64  `json:"version"`
	Duplicate bool   `json:"duplicate"`
}

type Job struct {
	ID         string `json:"id"`
	AttemptID  string `json:"attempt_id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	InputHash  string `json:"input_hash"`
	PatchHash  string `json:"patch_hash,omitempty"`
	TreeHash   string `json:"tree_hash,omitempty"`
	ConfigHash string `json:"config_hash"`
	ResultHash string `json:"result_hash,omitempty"`
	RunnerKind string `json:"runner_kind"`
	SessionID  string `json:"session_id,omitempty"`
}

type Effect struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	AttemptID   string `json:"attempt_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	Kind        string `json:"kind"`
	PayloadHash string `json:"payload_hash"`
	Nonce       string `json:"nonce"`
	State       string `json:"state"`
	Fence       int64  `json:"fence"`
}

// Observed are the facts only the controller can establish: what a transport
// saw, what the scheduler may do, which policies the run was configured with.
// A builder cannot supply them. The zero value refuses everything.
type Observed struct {
	SessionSettled    bool
	WriterOwned       bool
	SchedulingAllowed bool
	NoCheckPolicy     bool
	NoReviewPolicy    bool
	// RequireVerification is set from the run's pinned acceptance policy: the
	// planner's independent verification is a requirement of acceptance.
	RequireVerification bool
	Budget              BudgetState
	NowMS               int64
}

// Facts are the trusted inputs to Reduce and Accepted. Only store and
// controller code builds them, from persisted records plus Observed. Builders
// cannot supply this struct through the mailbox.
//
// CheckPassed and ReviewStatus carry the outcome recorded in the certificate
// and review result; the event names which job finished, never how it went.
type Facts struct {
	IdentityValid         bool
	SessionSettled        bool
	SnapshotComplete      bool
	PatchComplete         bool
	CheckCertificateValid bool
	CheckPassed           bool
	ReviewResultValid     bool
	ReviewStatus          ReviewState
	// VerificationRequired: the run's acceptance policy needs an independent
	// verification. VerificationValid: a completed verification result is
	// bound to this attempt's patch and tree and came from a reviewer who is
	// not the writer. VerificationStatus is its recorded outcome. Verification
	// has no waiver: nothing but a valid, passed result satisfies it.
	VerificationRequired bool
	VerificationValid    bool
	VerificationStatus   ReviewState
	NoCheckPolicy        bool
	NoReviewPolicy       bool
	ImplementationWaiver bool
	CheckWaiver          bool
	ReviewWaiver         bool
	WaiverTreeMatches    bool
	SchedulingAllowed    bool
	DependenciesAccepted bool
	WriterOwned          bool
	RepairAllowed        bool
	Budget               BudgetState
	NowMS                int64
}

// EffectIntent is work a transition asks for. InputHash names the artifact the
// work operates on; empty means the event's own payload.
type EffectIntent struct {
	Kind      string
	JobKind   string
	InputHash string
}

// Transition is the result of reducing one event.
type Transition struct {
	Attempt   Attempt
	PauseCode string
	Effects   []EffectIntent
	Accepted  bool
}
