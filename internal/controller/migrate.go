package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// importedRecord is where an imported build's record is kept. Its name no
// longer matches what the engine selector looks for, so the legacy engine does
// not see a build that now belongs to the durable run.
const importedRecord = "build.json.imported"

// ActiveImport asks to carry a stopped legacy build into a durable run.
type ActiveImport struct {
	// Identity names the checkout the history is recorded under.
	Identity state.Identity
	// Root is the checkout whose relay holds the build.
	Root string
	// Quiescent is the operator's assertion that nothing is writing. It is a
	// precondition, never the proof: the checks below still run.
	Quiescent bool
	// Probe reports the status of the legacy builder's pane. Nil means there is
	// no way to look, which is a refusal.
	Probe func(ctx context.Context, pane string) (string, error)
	// BuilderKind overrides the kind recorded in the relay's builder.json.
	BuilderKind string
	// ReviewerKind and ReviewerSession override the reviewer pane recorded in
	// the relay's reviewer.json. A run needs one: the builder never reviews.
	ReviewerKind, ReviewerSession string
}

// ActiveImportResult says what an import did.
type ActiveImportResult struct {
	RunID        string   `json:"run_id"`
	HistoryRunID string   `json:"history_run_id"`
	Carried      []string `json:"carried"`
	Resume       string   `json:"resume"`
	Unresolved   []string `json:"unresolved"`
}

// unresolvedEvidence is what a carried task is missing: the legacy engine
// either never recorded it or recorded only hashes with no bytes behind them.
var unresolvedEvidence = []string{"start_snapshot", "end_snapshot", "patch", "check_certificate", "review_result"}

// ImportActive carries a stopped legacy build into a new durable run.
//
// It refuses, creating nothing, unless the legacy build and builder are
// provably still: stopped, unchanged while the pane is checked, the pane idle
// or done, the plan unchanged, and no durable run or writer lease. An expired
// lease means reconcile, never "no writer".
//
// The legacy build is archived as a read-only history run. Finished tasks are
// carried with one accepted attempt each and no evidence, which the legacy
// engine never captured. The first unfinished task starts fresh from the
// current tree.
func (c *Controller) ImportActive(ctx context.Context, req ActiveImport) (ActiveImportResult, error) {
	relay := paths.Relay(req.Root)
	dir := build.Dir(relay)
	b, err := build.Load(dir)
	if err != nil {
		return ActiveImportResult{}, err
	}
	if b == nil {
		return ActiveImportResult{}, fmt.Errorf("%w: there is no legacy build in %s", workflow.ErrConflict, relay)
	}
	recordBefore, err := os.ReadFile(filepath.Join(dir, "build.json"))
	if err != nil {
		return ActiveImportResult{}, err
	}
	if err := c.proveQuiescent(ctx, req, relay, b, recordBefore); err != nil {
		return ActiveImportResult{}, err
	}
	planRaw, err := os.ReadFile(b.Plan)
	if err != nil {
		return ActiveImportResult{}, fmt.Errorf("%w: the build's plan cannot be read: %v", workflow.ErrConflict, err)
	}
	if sum := sha256.Sum256(planRaw); hex.EncodeToString(sum[:]) != b.PlanSHA {
		return ActiveImportResult{}, fmt.Errorf("%w: %s changed since the build started; import it only against the plan it was built from", workflow.ErrConflict, b.Plan)
	}
	kind, err := builderKind(req, relay)
	if err != nil {
		return ActiveImportResult{}, err
	}

	// A legacy build had no reviewer pane. The request's, or the one tandem-pair
	// recorded, is read and checked here, before the archive below moves
	// anything: Start would refuse it only after that.
	reviewerKind, reviewerSession, err := RecordedReviewerPane(relay, StartRequest{BuilderSession: b.Builder, PlannerSession: b.Planner,
		ReviewerKind: req.ReviewerKind, ReviewerSession: req.ReviewerSession})
	if err != nil {
		return ActiveImportResult{}, err
	}

	// Preserve first: the archive is verified byte for byte and recorded as a
	// history run before anything else changes.
	arch, err := build.Archive(req.Root, dir)
	if err != nil {
		return ActiveImportResult{}, err
	}
	hist, err := c.d.State.ImportLegacy(ctx, req.Identity, arch.Dir)
	if err != nil {
		return ActiveImportResult{}, err
	}
	recordHash, err := c.put(ctx, string(recordBefore))
	if err != nil {
		return ActiveImportResult{}, err
	}
	note, err := c.put(ctx, "accepted by the legacy engine; its start snapshot, end snapshot, patch, check certificate and review result were not recorded in a form that can be verified\n")
	if err != nil {
		return ActiveImportResult{}, err
	}

	// The legacy engine numbered tasks by position; the plan names them.
	specs := plan.Import(string(planRaw)).Specs
	if len(specs) != len(b.Tasks) {
		return ActiveImportResult{}, fmt.Errorf("%w: the build has %d tasks and its plan %d", workflow.ErrConflict, len(b.Tasks), len(specs))
	}
	done := carried{}
	var order []string
	for i, t := range b.Tasks {
		if t.Status == build.StatusDone {
			id := specs[i].ID
			done[id] = [][2]string{{"legacy_record", recordHash}, {"unresolved", note}}
			order = append(order, id)
		}
	}
	runID, err := c.startRun(ctx, StartRequest{Root: req.Root, PlanPath: b.Plan, BuilderKind: kind, BuilderSession: b.Builder,
		PlannerSession: b.Planner, CheckCommand: b.Check, TaskInputLimit: b.Budget,
		ReviewerKind: reviewerKind, ReviewerSession: reviewerSession}, done)
	if err != nil {
		return ActiveImportResult{}, err
	}
	// The legacy record moves out of the engine selector's way. A crash before
	// this leaves both, which the selector refuses; nothing is lost.
	if err := os.Rename(filepath.Join(dir, "build.json"), filepath.Join(dir, importedRecord)); err != nil {
		return ActiveImportResult{}, fmt.Errorf("the durable run %s was created but the legacy record could not be set aside: %w", runID, err)
	}
	res := ActiveImportResult{RunID: runID, HistoryRunID: hist.RunID, Carried: order, Unresolved: unresolvedEvidence}
	if tasks, terr := c.d.State.RunTasks(ctx, runID); terr == nil {
		for _, t := range tasks {
			if t.AcceptedAttemptID == "" {
				res.Resume = t.ID
				break
			}
		}
	}
	return res, nil
}

func builderKind(req ActiveImport, relay string) (string, error) {
	if req.BuilderKind != "" {
		return req.BuilderKind, nil
	}
	raw, err := os.ReadFile(filepath.Join(relay, "builder.json"))
	if err != nil {
		return "", fmt.Errorf("%w: the builder's kind is not recorded in %s; pass it explicitly", workflow.ErrConflict, relay)
	}
	var rec struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Kind == "" {
		return "", fmt.Errorf("%w: %s names no builder kind", workflow.ErrConflict, filepath.Join(relay, "builder.json"))
	}
	return rec.Kind, nil
}

func (c *Controller) proveQuiescent(ctx context.Context, req ActiveImport, relay string, b *build.Build, record []byte) error {
	busy := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{workflow.ErrBusy}, a...)...)
	}
	switch {
	case !req.Quiescent:
		return busy("nothing asserts that the legacy build is quiescent; stop it and pass --quiescent")
	case b.State == build.StateRunning:
		return busy("the legacy build %s is running; stop it with `tandem build stop` first", b.ID)
	case b.State == build.StateFinished:
		return busy("the legacy build %s is finished; import its archive with `tandem state import-legacy --archive`", b.ID)
	case b.State != build.StateStopped:
		return busy("the legacy build %s is %q, not stopped", b.ID, b.State)
	case req.Probe == nil:
		return busy("there is no way to look at the builder %q, so a writer cannot be ruled out", b.Builder)
	}
	if run, err := ReadActiveRun(relay); err != nil {
		return err
	} else if run != nil {
		return busy("%s already has a durable run (%s)", relay, run.RunID)
	}
	if lease, err := c.d.State.LeaseState(ctx, c.d.State.CheckoutID()); err != nil {
		return err
	} else if lease.State == "owned" || lease.State == "reconciling" {
		return busy("the checkout's writer lease is %s by %s; an expired lease is not proof that its writer stopped", lease.State, lease.Owner)
	}
	status, err := req.Probe(ctx, b.Builder)
	if err != nil {
		return busy("the builder %q could not be observed (%v), so a writer cannot be ruled out", b.Builder, err)
	}
	if !agent.Settled(status) {
		return busy("the builder %q is %q; wait until it is idle", b.Builder, status)
	}
	// The record must not have changed while the pane was looked at.
	again, err := os.ReadFile(filepath.Join(build.Dir(relay), "build.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(again, record) {
		return busy("the legacy record changed while it was being checked")
	}
	return nil
}
