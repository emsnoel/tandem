package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/workflow"
)

const importPlan = "# Greeting\n\n**Goal:** greet.\n\n### Task 1: Punctuate\n\none\n\n### Task 2: Farewell\n\ntwo\n"

// legacyRepo makes a repository whose relay holds a finished two-task build
// with a report, a review report and a check log per task, then archives it.
func legacyArchive(t *testing.T, state string, mutate func(b *build.Build)) (root, archive string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	dir := filepath.Join(root, ".tandem-relay", "build")
	b, _, err := build.Start(dir, plan.Split(importPlan), "/plans/p.md", "planshaabc", build.Options{
		ID: "b1", Builder: "builder", Planner: "planner", Check: "./check.sh", Budget: 1000, Judge: true,
		Now: "2026-09-28T23:00:00Z", HistoryRoot: root,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "checks"), 0o755); err != nil {
		t.Fatal(err)
	}
	zero := 0
	for i := range b.Tasks {
		task := &b.Tasks[i]
		write := func(name, body string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}
		n := string(rune('1' + i))
		task.Report = write("task-"+n+".report.md", state+" report "+n)
		task.ReviewReport = write("task-"+n+".review.md", state+" review "+n)
		task.CheckLog = write("checks/task-"+n+".log", state+" check "+n)
		task.Status, task.CheckExit = build.StatusDone, &zero
		task.Usage = &build.Usage{Calls: 3, Input: 500}
	}
	b.State = build.StateFinished
	if mutate != nil {
		mutate(b)
	}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	res, err := build.Archive(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	return root, res.Dir
}

func importer(t *testing.T) (*Store, Identity) {
	t.Helper()
	s := openStore(t, newRoot(t))
	s.now = fixedClock()
	return s, Identity{CheckoutID: hexID(900), CanonicalRoot: "/the/checkout", GitCommonDir: "/the/checkout/.git", ManifestRoot: "."}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	if out, err := exec.Command("cp", "-R", from, to).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
}

func table(t *testing.T, s *Store, name string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM ` + name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func counts(t *testing.T, s *Store) [4]int {
	return [4]int{table(t, s, "runs"), table(t, s, "tasks"), table(t, s, "imports"), table(t, s, "attempts")}
}

func TestLegacyImportRecordsAnArchivedRunWithoutInventingEvidence(t *testing.T) {
	_, archive := legacyArchive(t, "a", nil)
	s, id := importer(t)
	res, err := s.ImportLegacy(context.Background(), id, archive)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported || len(res.RunID) != 32 || res.Tasks != 2 || len(res.Digest) != 64 {
		t.Fatalf("result %+v", res)
	}
	var state string
	var engine int
	var archivedMS *int64
	s.db.QueryRow(`SELECT state, engine_version, archived_ms FROM runs WHERE id=?`, res.RunID).Scan(&state, &engine, &archivedMS)
	if state != "archived" || archivedMS == nil || *archivedMS != 5000 {
		t.Fatalf("run state %s archived_ms %v", state, archivedMS)
	}
	// No attempts, so nothing can be accepted, and no snapshot, patch,
	// certificate or review was made up.
	for _, tbl := range []string{"attempts", "jobs", "check_certificates", "review_results", "effects", "events", "usage_records", "waivers"} {
		if n := table(t, s, tbl); n != 0 {
			t.Errorf("%s has %d rows after an import", tbl, n)
		}
	}
	var accepted int
	s.db.QueryRow(`SELECT count(*) FROM tasks WHERE accepted_attempt_id IS NOT NULL`).Scan(&accepted)
	if accepted != 0 {
		t.Fatal("an imported task is marked accepted")
	}
	var receipt string
	s.db.QueryRow(`SELECT source_digest FROM imports WHERE run_id=?`, res.RunID).Scan(&receipt)
	if receipt != res.Digest {
		t.Fatalf("receipt digest %s", receipt)
	}
}

func TestLegacyProvenanceMarksUnverifiableFieldsUnknown(t *testing.T) {
	_, archive := legacyArchive(t, "p", nil)
	s, id := importer(t)
	res, err := s.ImportLegacy(context.Background(), id, archive)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.db.QueryRow(`SELECT hash FROM artifact_refs WHERE owner_kind='run' AND owner_id=? AND role='provenance'`, res.RunID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	chunk, err := artifactReader(t, s).Read(context.Background(), hash, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := string(chunk.Data)
	for _, want := range []string{`"schema": "legacy-import/v1"`, `"legacy_status": "done"`, `"start_snapshot"`, `"check_certificate"`, `"review_result"`, `"unknown"`} {
		if !strings.Contains(body, want) {
			t.Errorf("provenance lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"review_result": "passed"`) {
		t.Error("provenance claims a verified review")
	}
}

func TestLegacyArchiveImportIsIdempotent(t *testing.T) {
	root, archive := legacyArchive(t, "i", nil)
	s, id := importer(t)
	first, err := s.ImportLegacy(context.Background(), id, archive)
	if err != nil || !first.Imported {
		t.Fatalf("first: %+v %v", first, err)
	}
	before := counts(t, s)

	// The same archive under another path: the digest is of content, not names.
	moved := filepath.Join(t.TempDir(), "another-name-entirely")
	copyDir(t, archive, moved)
	second, err := s.ImportLegacy(context.Background(), id, moved)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported || second.RunID != first.RunID || second.Digest != first.Digest {
		t.Fatalf("second import %+v, first %+v", second, first)
	}
	// And the same build archived a second time, which rewrites paths to a new
	// directory but holds the same evidence.
	again, err := build.Archive(root, filepath.Join(root, ".tandem-relay", "build"))
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.ImportLegacy(context.Background(), id, again.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if third.Imported || third.RunID != first.RunID {
		t.Fatalf("a second archive of the same build imported again: %+v", third)
	}
	if counts(t, s) != before {
		t.Fatalf("repeat imports changed state: %v -> %v", before, counts(t, s))
	}
}

func TestDifferentEvidenceIsADifferentImport(t *testing.T) {
	_, a := legacyArchive(t, "one", nil)
	_, b := legacyArchive(t, "two", nil)
	s, id := importer(t)
	r1, _ := s.ImportLegacy(context.Background(), id, a)
	r2, err := s.ImportLegacy(context.Background(), id, b)
	if err != nil || !r2.Imported || r1.RunID == r2.RunID || r1.Digest == r2.Digest {
		t.Fatalf("%+v %+v %v", r1, r2, err)
	}
	if table(t, s, "runs") != 2 || table(t, s, "imports") != 2 {
		t.Fatalf("counts %v", counts(t, s))
	}
}

// If the archive changes between the first read and the receipt, the bytes
// copied may not be the bytes verified, so no import may be recorded.
func TestLegacyArchiveChangedDuringImport(t *testing.T) {
	_, archive := legacyArchive(t, "c", nil)
	s, id := importer(t)
	s.importHooks.afterCopy = func() {
		target := filepath.Join(archive, "build", "task-1.report.md")
		os.Chmod(target, 0o644)
		if err := os.WriteFile(target, []byte("edited after it was read"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.ImportLegacy(context.Background(), id, archive)
	if err == nil || !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := counts(t, s); got != [4]int{} {
		t.Fatalf("a receipt or run was written: %v", got)
	}
	// Once the archive is stable again (restored) the import completes.
	s.importHooks.afterCopy = nil
	_, fresh := legacyArchive(t, "c", nil)
	if res, err := s.ImportLegacy(context.Background(), id, fresh); err != nil || !res.Imported {
		t.Fatalf("a stable archive: %+v %v", res, err)
	}
}

func TestLegacyImportIsAllOrNothing(t *testing.T) {
	_, archive := legacyArchive(t, "t", nil)
	s, id := importer(t)
	s.importHooks.beforeCommit = func() error { return errors.New("crash before commit") }
	if _, err := s.ImportLegacy(context.Background(), id, archive); err == nil {
		t.Fatal("expected the injected failure")
	}
	if got := counts(t, s); got != [4]int{} {
		t.Fatalf("partial import: %v", got)
	}
	for _, tbl := range []string{"artifact_refs", "checkouts", "projects"} {
		if n := table(t, s, tbl); n != 0 {
			t.Errorf("%s kept %d rows from the failed import", tbl, n)
		}
	}
	s.importHooks.beforeCommit = nil
	if res, err := s.ImportLegacy(context.Background(), id, archive); err != nil || !res.Imported {
		t.Fatalf("retry: %+v %v", res, err)
	}
}

func TestLegacyImportRefusesWhatItCannotVouchFor(t *testing.T) {
	ctx := context.Background()
	t.Run("a running build", func(t *testing.T) {
		_, archive := legacyArchive(t, "r", func(b *build.Build) { b.State = build.StateRunning })
		s, id := importer(t)
		if _, err := s.ImportLegacy(ctx, id, archive); !errors.Is(err, workflow.ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
		if counts(t, s) != [4]int{} {
			t.Fatal("an active build was imported")
		}
	})
	t.Run("a stopped build imports", func(t *testing.T) {
		_, archive := legacyArchive(t, "s", func(b *build.Build) {
			b.State, b.StopReason, b.StopTask = build.StateStopped, "budget", 1
			b.Tasks[1].Status = build.StatusPending
		})
		s, id := importer(t)
		if res, err := s.ImportLegacy(ctx, id, archive); err != nil || !res.Imported || res.Tasks != 2 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("a tampered file", func(t *testing.T) {
		_, archive := legacyArchive(t, "x", nil)
		target := filepath.Join(archive, "build", "task-2.report.md")
		os.Chmod(target, 0o644)
		os.WriteFile(target, []byte("not what was archived"), 0o644)
		s, id := importer(t)
		if _, err := s.ImportLegacy(ctx, id, archive); err == nil {
			t.Fatal("tampered evidence was imported")
		}
		if counts(t, s) != [4]int{} || table(t, s, "artifacts") != 0 {
			t.Fatal("a failed verification still wrote rows")
		}
	})
	t.Run("a symlinked file", func(t *testing.T) {
		_, archive := legacyArchive(t, "l", nil)
		target := filepath.Join(archive, "build", "task-1.report.md")
		body, _ := os.ReadFile(target)
		outside := filepath.Join(t.TempDir(), "outside.md")
		os.WriteFile(outside, body, 0o644)
		os.Remove(target)
		if err := os.Symlink(outside, target); err != nil {
			t.Fatal(err)
		}
		s, id := importer(t)
		if _, err := s.ImportLegacy(ctx, id, archive); err == nil {
			t.Fatal("a symlink in the archive was followed")
		}
	})
	t.Run("not an archive", func(t *testing.T) {
		s, id := importer(t)
		if _, err := s.ImportLegacy(ctx, id, t.TempDir()); err == nil {
			t.Fatal("an empty directory was imported")
		}
		if _, err := s.ImportLegacy(ctx, id, filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("a missing path was imported")
		}
	})
}

func TestLegacyImportStoresEveryArchivedFileAsAnArtifact(t *testing.T) {
	_, archive := legacyArchive(t, "f", nil)
	s, id := importer(t)
	if _, err := s.ImportLegacy(context.Background(), id, archive); err != nil {
		t.Fatal(err)
	}
	var m build.Manifest
	raw, _ := os.ReadFile(filepath.Join(archive, "manifest.json"))
	if err := jsonUnmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		var n int
		s.db.QueryRow(`SELECT count(*) FROM artifacts WHERE hash=?`, f.SHA256).Scan(&n)
		if n != 1 {
			t.Errorf("%s (%s) is not an artifact", f.Path, f.SHA256)
		}
		if _, err := artifactReader(t, s).Read(context.Background(), f.SHA256, 0, 1); err != nil {
			t.Errorf("%s: %v", f.Path, err)
		}
	}
	sum := sha256.Sum256(raw)
	var n int
	s.db.QueryRow(`SELECT count(*) FROM imports WHERE manifest_hash=?`, hex.EncodeToString(sum[:])).Scan(&n)
	if n != 1 {
		t.Error("the receipt does not name the manifest artifact")
	}
}

// The import keeps what the legacy reviewer pane decided: its verdict and how
// many rounds the task took, and which pane reviewed.
func TestLegacyProvenanceCarriesTheReviewVerdictAndRounds(t *testing.T) {
	reviewed := true
	_, archive := legacyArchive(t, "v", func(b *build.Build) {
		b.Reviewer = "critic"
		b.Tasks[0].Review = &build.Review{RecordID: "b1-t1-r2", Verdict: "review", Result: "clean", Reviewed: &reviewed, Delivered: true}
		b.Tasks[0].ReviewRounds = 2
	})
	s, id := importer(t)
	res, err := s.ImportLegacy(context.Background(), id, archive)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.db.QueryRow(`SELECT hash FROM artifact_refs WHERE owner_kind='run' AND owner_id=? AND role='provenance'`, res.RunID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	chunk, err := artifactReader(t, s).Read(context.Background(), hash, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Reviewer string `json:"reviewer"`
		Tasks    []struct {
			Review *struct {
				Verdict string `json:"verdict"`
				Result  string `json:"result"`
				Rounds  int    `json:"rounds"`
			} `json:"legacy_review"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(chunk.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Reviewer != "critic" || len(p.Tasks) == 0 || p.Tasks[0].Review == nil {
		t.Fatalf("provenance %s", chunk.Data)
	}
	if r := p.Tasks[0].Review; r.Verdict != "review" || r.Result != "clean" || r.Rounds != 2 {
		t.Fatalf("legacy_review = %+v, want the reviewer's clean verdict after 2 rounds", *r)
	}
}
