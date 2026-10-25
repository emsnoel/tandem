package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/snapshot"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// legacyStopped writes a stopped legacy build of the env's plan whose first task
// is done, with the dirty-file hashes the legacy engine recorded and no bytes
// behind them.
func (e *cenv) legacyStopped() string {
	e.t.Helper()
	raw, err := os.ReadFile(e.plan)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	dir := build.Dir(e.relay)
	b, _, err := build.Start(dir, plan.Split(string(raw)), e.plan, hex.EncodeToString(sum[:]), build.Options{
		ID: "legacy-1", Builder: "builder", Planner: "planner", Check: "true", HistoryRoot: e.root, Now: "2026-10-01T10:00:00Z"}, false)
	if err != nil {
		e.t.Fatal(err)
	}
	b.Tasks[0].Status, b.Tasks[0].TreeBefore, b.Tasks[0].Files = build.StatusDone, map[string]string{"work1.txt": "0123abcd"}, []string{"work1.txt"}
	b.Tasks[1].Status = build.StatusSent
	b.State, b.Current, b.StopReason = build.StateStopped, 2, "red"
	if err := b.Save(dir); err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(e.relay, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.relay, "builder.json"), []byte(`{"kind":"codex","autonomy":"auto"}`+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func (e *cenv) importRequest(probe string) ActiveImport {
	e.t.Helper()
	root, common, err := state.GitIdentity(e.root)
	if err != nil {
		e.t.Fatal(err)
	}
	return ActiveImport{
		Identity:     state.Identity{CheckoutID: e.st.CheckoutID(), CanonicalRoot: root, GitCommonDir: common, ManifestRoot: ".", StateRoot: filepath.Dir(filepath.Dir(e.relay))},
		Root:         e.root,
		Quiescent:    true,
		ReviewerKind: "claude", ReviewerSession: "reviewer-session",
		Probe: func(context.Context, string) (string, error) { return probe, nil },
	}
}

func TestActiveImportRejectsUncertainWriter(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(e *cenv, r *ActiveImport)
		want   error
		// edits is true when the case itself rewrites the legacy record.
		edits bool
	}{
		"quiescence not asserted": {func(e *cenv, r *ActiveImport) { r.Quiescent = false }, workflow.ErrBusy, false},
		"the builder is working": {func(e *cenv, r *ActiveImport) {
			r.Probe = func(context.Context, string) (string, error) { return "working", nil }
		}, workflow.ErrBusy, false},
		"the builder is blocked": {func(e *cenv, r *ActiveImport) {
			r.Probe = func(context.Context, string) (string, error) { return "blocked", nil }
		}, workflow.ErrBusy, false},
		"the status is unknown": {func(e *cenv, r *ActiveImport) {
			r.Probe = func(context.Context, string) (string, error) { return "", errors.New("herdr: no such pane") }
		}, workflow.ErrBusy, false},
		"there is no way to look": {func(e *cenv, r *ActiveImport) { r.Probe = nil }, workflow.ErrBusy, false},
		"the build is running": {func(e *cenv, r *ActiveImport) {
			dir := build.Dir(e.relay)
			b, _ := build.Load(dir)
			b.State = build.StateRunning
			if err := b.Save(dir); err != nil {
				e.t.Fatal(err)
			}
		}, workflow.ErrBusy, false},
		"the record changes while the pane is looked at": {func(e *cenv, r *ActiveImport) {
			r.Probe = func(context.Context, string) (string, error) {
				dir := build.Dir(e.relay)
				b, _ := build.Load(dir)
				b.StopReason = "someone else wrote this"
				if err := b.Save(dir); err != nil {
					e.t.Fatal(err)
				}
				return "idle", nil
			}
		}, workflow.ErrBusy, true},
		"no reviewer pane":                 {func(e *cenv, r *ActiveImport) { r.ReviewerKind, r.ReviewerSession = "", "" }, workflow.ErrConflict, false},
		"the reviewer pane is the builder": {func(e *cenv, r *ActiveImport) { r.ReviewerSession = "builder" }, workflow.ErrInvalidTransition, false},
		"the reviewer pane is the planner": {func(e *cenv, r *ActiveImport) { r.ReviewerSession = "planner" }, workflow.ErrInvalidTransition, false},
		"reviewer.json names the builder": {func(e *cenv, r *ActiveImport) {
			r.ReviewerKind, r.ReviewerSession = "", ""
			if err := os.WriteFile(filepath.Join(e.relay, "reviewer.json"), []byte(`{"kind":"codex","name":"builder"}`+"\n"), 0o644); err != nil {
				e.t.Fatal(err)
			}
		}, workflow.ErrInvalidTransition, false},
		"the plan changed since the build": {func(e *cenv, r *ActiveImport) { e.setPlan(twoTaskPlan + "\n### Task 3: Third\n\nmore\n") }, workflow.ErrConflict, false},
		"an expired lease is still a writer": {func(e *cenv, r *ActiveImport) {
			e.start()
			os.Remove(filepath.Join(e.relay, ActiveRunFile))
			os.RemoveAll(RunDir(e.relay, e.runID))
			e.clk.advance(time.Hour)
		}, workflow.ErrBusy, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			dir := e.legacyStopped()
			req := e.importRequest("idle")
			tc.mutate(e, &req)
			before, _ := os.ReadFile(filepath.Join(dir, "build.json"))
			runsBefore := e.countRuns()
			if _, err := e.c.ImportActive(ctx, req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "build.json"))
			if !tc.edits && string(before) != string(after) {
				t.Error("the legacy record was touched")
			}
			if ar, _ := ReadActiveRun(e.relay); ar != nil {
				t.Error("a durable run was started")
			}
			if got := e.countRuns(); got != runsBefore {
				t.Errorf("%d runs recorded, %d before: a refused import left rows", got, runsBefore)
			}
		})
	}
}

// countRuns counts every run row, archived history included.
func (e *cenv) countRuns() int {
	e.t.Helper()
	n, err := e.st.CountRuns(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestImportDoesNotInventSnapshot(t *testing.T) {
	e := newCEnv(t)
	dir := e.legacyStopped()
	res, err := e.c.ImportActive(ctx, e.importRequest("idle"))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = res.RunID
	if len(res.Carried) != 1 || res.Carried[0] != "task-1" || res.Resume != "task-2" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.json")); !os.IsNotExist(err) {
		t.Error("the legacy record is still where the engine looks for it")
	}
	if eng, err := SelectEngine(e.relay, ""); err != nil || eng != EngineDurable {
		t.Fatalf("engine %s %v", eng, err)
	}

	tasks := e.tasks()
	carried := e.current("task-1")
	if tasks[0].AcceptedAttemptID != carried.ID {
		t.Fatal("the legacy engine's acceptance of task 1 was not carried")
	}
	// Hashes of old dirty files are not a snapshot: nothing is made up.
	if carried.StartSnapshotHash != "" || carried.EndSnapshotHash != "" || carried.PatchHash != "" {
		t.Errorf("a snapshot or patch was invented for a task the legacy engine never captured: %+v", carried)
	}
	if carried.Check != workflow.CheckPending || carried.Review != workflow.ReviewPending {
		t.Errorf("check %s review %s: unverifiable history must stay unresolved, not passed", carried.Check, carried.Review)
	}
	if carried.Implementation == workflow.ImplementationRunning {
		t.Errorf("implementation %s", carried.Implementation)
	}

	// The resumed task is a new, identified attempt with a snapshot of today's tree.
	next := e.current("task-2")
	if next.ID == carried.ID || next.Ordinal != 1 || next.StartSnapshotHash == "" || next.Implementation != workflow.ImplementationPending {
		t.Errorf("resumed attempt %+v", next)
	}
	if as, _ := e.st.TaskAttempts(ctx, e.runID, "task-1"); len(as) != 1 {
		t.Errorf("%d attempts for the carried task: it must not be redone", len(as))
	}
	// The history is preserved as an archived run beside it.
	hist, err := e.st.GetRun(ctx, res.HistoryRunID)
	if err != nil || hist.State != "archived" {
		t.Fatalf("history run %+v %v", hist, err)
	}
	if e.countRuns() != 2 {
		t.Errorf("%d runs", e.countRuns())
	}
	if res.HistoryRunID == res.RunID {
		t.Error("history and the live run are one run")
	}
}

func TestImportedRunContinuesToTheEnd(t *testing.T) {
	e := newCEnv(t)
	e.legacyStopped()
	res, err := e.c.ImportActive(ctx, e.importRequest("done"))
	if err != nil {
		t.Fatal(err)
	}
	e.runID = res.RunID
	e.tick()
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Fatalf("%d builder prompts: only the unfinished task is sent", got)
	}
	att := e.toReview("task-2", 2)
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if st, code := e.runState(); st != "finished" || code != "" {
		t.Fatalf("run %s/%s", st, code)
	}
	if got := e.delivered()[workflow.EffectBuilderDispatch]; got != 1 {
		t.Errorf("%d builder prompts in all: the carried task was sent again", got)
	}
}

// exportEnv runs the first task of a durable run to an accepted review and
// leaves the second pending, so the run holds snapshots, a patch, a check log,
// a review and a sent prompt.
func (e *cenv) exportEnv() string {
	e.t.Helper()
	e.start()
	att := e.toReview("task-1", 1)
	if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
		e.t.Fatal(err)
	}
	e.tick()
	return filepath.Join(e.t.TempDir(), "export")
}

func TestExportIsSelfContained(t *testing.T) {
	e := newCEnv(t)
	out := e.exportEnv()
	m, err := e.c.Export(ctx, e.runID, out)
	if err != nil {
		t.Fatal(err)
	}
	// The source is gone: only the export may be read from here on.
	if err := os.RemoveAll(e.artRoot()); err != nil {
		t.Fatal(err)
	}
	exp, err := artifact.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Artifacts) < 10 {
		t.Fatalf("%d artifacts exported: a run with a patch, a check and a review has more", len(m.Artifacts))
	}
	have := map[string]bool{}
	for _, a := range m.Artifacts {
		raw, err := exp.ReadAll(ctx, a.Hash, 1<<28)
		if err != nil {
			t.Fatalf("exported artifact %s does not verify: %v", a.Hash, err)
		}
		if int64(len(raw)) != a.Size {
			t.Errorf("artifact %s is %d bytes, the manifest says %d", a.Hash, len(raw), a.Size)
		}
		have[a.Hash] = true
	}
	// Everything the run's rows and the snapshots they name point at is there.
	att := e.current("task-1")
	for role, h := range map[string]string{"start snapshot": att.StartSnapshotHash, "end snapshot": att.EndSnapshotHash, "patch": att.PatchHash} {
		if !have[h] {
			t.Errorf("the %s %s is not in the export", role, h)
		}
	}
	start, err := snapshot.LoadManifest(ctx, exp, att.StartSnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range start.Entries {
		if !have[en.ContentHash] {
			t.Errorf("file %s (%s) of the start snapshot is not in the export", en.PathString(), en.ContentHash)
		}
	}
	end, _ := snapshot.LoadManifest(ctx, exp, att.EndSnapshotHash)
	for _, en := range end.Entries {
		if !have[en.ContentHash] {
			t.Errorf("file %s of the end snapshot is not in the export", en.PathString())
		}
	}
	roles := map[string]bool{}
	for _, a := range m.Artifacts {
		for _, r := range a.Roles {
			roles[r] = true
		}
	}
	for _, r := range []string{"plan", "config", "packet", "start_snapshot", "end_snapshot", "patch", "check_log", "check_command", "review_result", "event", "effect_payload"} {
		if !roles[r] {
			t.Errorf("no %s artifact in the export", r)
		}
	}
	if _, err := os.Stat(filepath.Join(out, ExportComplete)); err != nil {
		t.Errorf("no completion marker: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(out, "manifest.json")); err != nil || !strings.Contains(string(raw), e.runID) {
		t.Errorf("manifest.json: %v", err)
	}
	if m.RunID != e.runID || len(m.Tasks) != 2 || m.Tasks[0].Accepted != att.ID {
		t.Errorf("manifest %+v", m)
	}
}

func TestExportRefusesToOverwriteAndPublishesLast(t *testing.T) {
	e := newCEnv(t)
	out := e.exportEnv()
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Export(ctx, e.runID, out); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("an existing destination: %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Error("the existing destination was written into")
	}
	// A failure before the end leaves no completion marker behind.
	e.c.hooks.beforeExportComplete = func() error { return errors.New("disk full") }
	fresh := filepath.Join(t.TempDir(), "again")
	if _, err := e.c.Export(ctx, e.runID, fresh); err == nil {
		t.Fatal("export succeeded past a failure")
	}
	if _, err := os.Stat(filepath.Join(fresh, ExportComplete)); !os.IsNotExist(err) {
		t.Error("a failed export is marked complete")
	}
}

func TestExportKeepsUnresolvedDelivery(t *testing.T) {
	e := newCEnv(t)
	e.start()
	e.tick() // the prompt is sent and never acknowledged
	e.clk.advance(state.AckWindow + time.Minute)
	e.tick()
	if st, code := e.runState(); st != "paused" || code != "writer_unknown" {
		t.Fatalf("setup: %s/%s", st, code)
	}
	m, err := e.c.Export(ctx, e.runID, filepath.Join(t.TempDir(), "x"))
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "paused" || m.PauseCode != "writer_unknown" {
		t.Errorf("%s/%s", m.State, m.PauseCode)
	}
	if len(m.Unresolved) != 1 || !strings.Contains(m.Unresolved[0], "delivery_unknown") {
		t.Errorf("an uncertain delivery must be named, not dropped: %v", m.Unresolved)
	}
	var found bool
	for _, x := range m.Effects {
		found = found || x.State == "delivery_unknown"
	}
	if !found {
		t.Errorf("effects %+v", m.Effects)
	}
}

func TestExportRefusesACorruptSource(t *testing.T) {
	e := newCEnv(t)
	out := e.exportEnv()
	att := e.current("task-1")
	path := filepath.Join(e.artRoot(), "sha256", att.PatchHash[:2], att.PatchHash)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not the patch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Export(ctx, e.runID, out); !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("a corrupt artifact was exported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ExportComplete)); !os.IsNotExist(err) {
		t.Error("a failed export is marked complete")
	}
}

// With no reviewer recorded, the refusal names both remedies.
func TestImportWithoutAReviewerNamesBothRemedies(t *testing.T) {
	e := newCEnv(t)
	e.legacyStopped()
	req := e.importRequest("idle")
	req.ReviewerKind, req.ReviewerSession = "", ""
	_, err := e.c.ImportActive(ctx, req)
	if err == nil {
		t.Fatal("an import with no reviewer was not refused")
	}
	for _, want := range []string{`"reviewer" block in .tandem.json`, "tandem-pair --reviewer KIND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
}
