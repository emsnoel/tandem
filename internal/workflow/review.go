package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Review statuses a reviewer may report.
const (
	ReviewStatusPassed           = "passed"
	ReviewStatusChangesRequested = "changes_requested"
	ReviewStatusFailed           = "failed"
)

var (
	reviewID   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reviewHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Severities and dispositions a finding may carry. Critical and high findings
// block: a review cannot pass while one is unresolved.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityInfo     = "info"

	DispositionOpen     = "open"
	DispositionFixed    = "fixed"
	DispositionRejected = "rejected"
)

const (
	// maxFindings and maxFindingMessage bound what one review result may say.
	maxFindings       = 200
	maxFindingMessage = 4096
)

// Blocking reports whether a finding of this severity stops a review passing.
func Blocking(severity string) bool { return severity == SeverityCritical || severity == SeverityHigh }

// Finding is one issue a reviewer reports: how serious it is, where it is, what
// is wrong and where it stands. Start and end lines are optional and 1-based; a
// finding with only a path is about the whole file.
type Finding struct {
	Severity    string `json:"severity"`
	Path        string `json:"path"`
	Disposition string `json:"disposition"`
	Message     string `json:"message"`
	StartLine   *int   `json:"start_line"`
	EndLine     *int   `json:"end_line"`
}

// ReviewResult is the structured outcome of a pane review. It is bound to the
// job that ran it and to the exact patch and tree it examined.
type ReviewResult struct {
	SchemaVersion int       `json:"schema_version"`
	JobID         string    `json:"job_id"`
	AttemptID     string    `json:"attempt_id"`
	PatchHash     string    `json:"patch_hash"`
	TreeHash      string    `json:"tree_hash"`
	Status        string    `json:"status"`
	Findings      []Finding `json:"findings"`
}

// Verdict is the reviewer's outcome, without controller-owned job identity.
type Verdict struct {
	SchemaVersion int       `json:"schema_version"`
	Status        string    `json:"status"`
	Findings      []Finding `json:"findings"`
}

// BindReview binds a verdict to the stored job that requested it.
func BindReview(v Verdict, job Job) ReviewResult {
	return ReviewResult{SchemaVersion: v.SchemaVersion, JobID: job.ID, AttemptID: job.AttemptID,
		PatchHash: job.PatchHash, TreeHash: job.TreeHash, Status: v.Status, Findings: v.Findings}
}

// DecodeReview accepts a verdict or a complete review result. Any supplied
// identity requires complete identity and must match the stored job.
func DecodeReview(raw []byte, job Job) (ReviewResult, error) {
	r, hasIdentity, err := decodeReviewObject(raw)
	if err != nil {
		return ReviewResult{}, err
	}
	if !hasIdentity {
		r = BindReview(Verdict{SchemaVersion: r.SchemaVersion, Status: r.Status, Findings: r.Findings}, job)
	}
	if err := ValidateReview(r, job); err != nil {
		return ReviewResult{}, err
	}
	return r, nil
}

// ValidateVerdictRaw checks a handback before queueing, without access to the
// stored job. Complete results must still supply correctly formatted identity.
func ValidateVerdictRaw(raw []byte) error {
	r, hasIdentity, err := decodeReviewObject(raw)
	if err != nil {
		return err
	}
	job := Job{ID: strings.Repeat("0", 32), AttemptID: strings.Repeat("0", 32),
		PatchHash: strings.Repeat("0", 64), TreeHash: strings.Repeat("0", 64), Kind: "reviewer"}
	if hasIdentity {
		job.ID, job.AttemptID, job.PatchHash, job.TreeHash = r.JobID, r.AttemptID, r.PatchHash, r.TreeHash
	} else {
		r = BindReview(Verdict{SchemaVersion: r.SchemaVersion, Status: r.Status, Findings: r.Findings}, job)
	}
	return ValidateReview(r, job)
}

func decodeReviewObject(raw []byte) (ReviewResult, bool, error) {
	bad := func(err error) (ReviewResult, bool, error) {
		return ReviewResult{}, false, fmt.Errorf("%w: review result: %v", ErrIncompleteEvidence, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return bad(err)
	}
	if fields == nil {
		return bad(fmt.Errorf("must be one JSON object"))
	}
	hasIdentity := false
	for key := range fields {
		switch key {
		case "job_id", "attempt_id", "patch_hash", "tree_hash":
			hasIdentity = true
		case "schema_version", "status", "findings":
		default:
			return bad(fmt.Errorf("unknown field %q", key))
		}
	}
	var result ReviewResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return bad(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return bad(fmt.Errorf("must be one JSON object"))
	}
	return result, hasIdentity, nil
}

// ValidateReview checks a result's structure and that it belongs to job: the
// same job, attempt, patch and tree. It is pure and never accepts a task; the
// controller still checks the attempt is current and the job really finished.
func ValidateReview(r ReviewResult, job Job) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: review result: %s", ErrIncompleteEvidence, fmt.Sprintf(format, a...))
	}
	switch {
	case r.SchemaVersion != 1:
		return bad("schema_version %d; this reads 1", r.SchemaVersion)
	case !reviewID.MatchString(r.JobID) || !reviewID.MatchString(r.AttemptID):
		return bad("job_id and attempt_id must be 32 lowercase hex characters")
	case !reviewHash.MatchString(r.PatchHash) || !reviewHash.MatchString(r.TreeHash):
		return bad("patch_hash and tree_hash must be 64 lowercase hex characters")
	case r.Status != ReviewStatusPassed && r.Status != ReviewStatusChangesRequested && r.Status != ReviewStatusFailed:
		return bad("status %q is not a reviewer outcome", r.Status)
	case r.Status == ReviewStatusChangesRequested && len(r.Findings) == 0:
		return bad("changes_requested must say what to change")
	case r.JobID != job.ID || r.AttemptID != job.AttemptID:
		return bad("it names job %s of attempt %s, not job %s of attempt %s", r.JobID, r.AttemptID, job.ID, job.AttemptID)
	case r.PatchHash != job.PatchHash || r.TreeHash != job.TreeHash:
		return bad("it reviewed a different patch or tree than this job was given")
	case job.Kind != "reviewer":
		return bad("job %s is a %s job", job.ID, job.Kind)
	case len(r.Findings) > maxFindings:
		return bad("%d findings; at most %d are accepted", len(r.Findings), maxFindings)
	}
	for i, f := range r.Findings {
		if err := validateFinding(f); err != nil {
			return bad("finding %d: %v", i+1, err)
		}
	}
	if r.Status == ReviewStatusPassed {
		for i, f := range r.Findings {
			switch {
			case !Blocking(f.Severity) || f.Disposition == DispositionFixed:
			case f.Disposition == DispositionRejected:
				return bad("finding %d is a %s finding that was rejected, and only a planner can record a reason that lets it stand; a pass needs the planner's review waiver", i+1, f.Severity)
			default:
				return bad("finding %d is a %s finding that is still unresolved, so this cannot be a pass", i+1, f.Severity)
			}
		}
	}
	return nil
}

func validateFinding(f Finding) error {
	switch f.Severity {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo:
	default:
		return fmt.Errorf("severity %q is not critical, high, medium, low or info", f.Severity)
	}
	switch f.Disposition {
	case DispositionOpen, DispositionFixed, DispositionRejected:
	default:
		return fmt.Errorf("disposition %q is not open, fixed or rejected", f.Disposition)
	}
	if f.Path == "" {
		return fmt.Errorf("path is empty")
	}
	if !utf8.ValidString(f.Path) {
		return fmt.Errorf("path is not valid UTF-8")
	}
	if err := ValidRelPath(f.Path); err != nil {
		return fmt.Errorf("path %q %v", f.Path, err)
	}
	if f.StartLine != nil && *f.StartLine < 1 {
		return fmt.Errorf("start_line %d is not a line number", *f.StartLine)
	}
	if f.EndLine != nil {
		switch {
		case f.StartLine == nil:
			return fmt.Errorf("end_line without start_line")
		case *f.EndLine < 1:
			return fmt.Errorf("end_line %d is not a line number", *f.EndLine)
		case *f.EndLine < *f.StartLine:
			return fmt.Errorf("end_line %d is before start_line %d", *f.EndLine, *f.StartLine)
		}
	}
	switch {
	case strings.TrimSpace(f.Message) == "":
		return fmt.Errorf("message is empty")
	case len(f.Message) > maxFindingMessage:
		return fmt.Errorf("message is %d bytes; at most %d", len(f.Message), maxFindingMessage)
	case !utf8.ValidString(f.Message) || strings.ContainsRune(f.Message, 0):
		return fmt.Errorf("message is not valid text")
	}
	return nil
}
