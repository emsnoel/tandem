package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// gcEnv is a store with a run (so some artifacts are referenced) and a way to
// add artifacts nothing refers to.
type gcEnv struct {
	s    *Store
	sd   *seed
	root string
}

func newGCEnv(t *testing.T) *gcEnv {
	t.Helper()
	s, sd, root := newSeededStore(t)
	// The seeded run is running; GC needs an idle checkout.
	sd.exec(t, `UPDATE runs SET state='finished'`)
	return &gcEnv{s: s, sd: sd, root: root}
}

func (e *gcEnv) orphan(t *testing.T, content string) string {
	t.Helper()
	return e.sd.artifact(t, content)
}

func (e *gcEnv) file(hash string) string {
	return filepath.Join(e.root, "artifacts", "sha256", hash[:2], hash)
}

func (e *gcEnv) exists(hash string) bool {
	_, err := os.Stat(e.file(hash))
	return err == nil
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestGCDryRunListsOnlyWhatNothingRefersTo(t *testing.T) {
	e := newGCEnv(t)
	lone := e.orphan(t, "nothing refers to this")
	rep, err := e.s.GC(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Apply || len(rep.Deleted) != 0 {
		t.Fatalf("a dry run: %+v", rep)
	}
	if !has(rep.Candidates, lone) {
		t.Errorf("the unreferenced artifact is not a candidate: %v", rep.Candidates)
	}
	for _, h := range []string{e.sd.StartHash, e.sd.PacketHash} {
		if has(rep.Candidates, h) {
			t.Errorf("%s is referenced by a row and must not be a candidate", h)
		}
	}
	if rep.Retained < 3 {
		t.Errorf("retained %d", rep.Retained)
	}
	if !e.exists(lone) {
		t.Error("a dry run deleted a file")
	}
	for i := 1; i < len(rep.Candidates); i++ {
		if rep.Candidates[i-1] >= rep.Candidates[i] {
			t.Fatalf("the report is not sorted: %v", rep.Candidates)
		}
	}
}

func TestGCApplyDeletesOnlyTheUnreferenced(t *testing.T) {
	e := newGCEnv(t)
	a, b := e.orphan(t, "orphan a"), e.orphan(t, "orphan b")
	rep, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !has(rep.Deleted, a) || !has(rep.Deleted, b) || len(rep.Deleted) != len(rep.Candidates) {
		t.Fatalf("%+v", rep)
	}
	if e.exists(a) || e.exists(b) {
		t.Error("an orphan's file survived")
	}
	var n int
	e.s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE hash IN (?,?)`, a, b).Scan(&n)
	if n != 0 {
		t.Error("an orphan's metadata survived")
	}
	for _, h := range []string{e.sd.StartHash, e.sd.PacketHash} {
		if !e.exists(h) {
			t.Errorf("referenced artifact %s was deleted", h)
		}
	}
	again, err := e.s.GC(ctx, true)
	if err != nil || len(again.Candidates) != 0 || len(again.Deleted) != 0 {
		t.Errorf("a second pass: %+v %v", again, err)
	}
}

func TestGCReferenceAddedAfterDryRunSurvives(t *testing.T) {
	e := newGCEnv(t)
	late := e.orphan(t, "referenced between the dry run and the apply")
	rep, err := e.s.GC(ctx, false)
	if err != nil || !has(rep.Candidates, late) {
		t.Fatalf("setup: %+v %v", rep, err)
	}
	e.sd.exec(t, `INSERT INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('run',?,'late',?)`, e.sd.RunID, late)
	applied, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if has(applied.Deleted, late) || !e.exists(late) {
		t.Fatal("an artifact referenced after the dry run was deleted: the references must be recomputed at apply time")
	}
}

func TestGCRefusesActivePublication(t *testing.T) {
	e := newGCEnv(t)
	lone := e.orphan(t, "mid-publication")
	locker, err := OpenLocker(e.root)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := locker.LockShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.GC(ctx, true); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("apply beside a publisher: %v", err)
	}
	if !e.exists(lone) {
		t.Error("something was deleted beside an active publisher")
	}
	// A dry run only reads, so it is not refused.
	if _, err := e.s.GC(ctx, false); err != nil {
		t.Errorf("dry run: %v", err)
	}
	unlock()
	if rep, err := e.s.GC(ctx, true); err != nil || !has(rep.Deleted, lone) {
		t.Errorf("after the publisher finished: %+v %v", rep, err)
	}
}

func TestGCNeedsAnIdleCheckout(t *testing.T) {
	for name, set := range map[string]func(e *gcEnv, t *testing.T){
		"a running run":  func(e *gcEnv, t *testing.T) { e.sd.exec(t, `UPDATE runs SET state='running'`) },
		"a stopping run": func(e *gcEnv, t *testing.T) { e.sd.exec(t, `UPDATE runs SET state='stopping'`) },
		"a held lease": func(e *gcEnv, t *testing.T) {
			if _, err := e.s.AcquireLease(ctx, e.sd.CheckoutID, "someone", 1); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newGCEnv(t)
			lone := e.orphan(t, "kept while busy")
			set(e, t)
			if _, err := e.s.GC(ctx, true); !errors.Is(err, workflow.ErrBusy) {
				t.Fatalf("err = %v", err)
			}
			if !e.exists(lone) {
				t.Error("deleted from a busy checkout")
			}
			// Reporting is still allowed.
			if _, err := e.s.GC(ctx, false); err != nil {
				t.Errorf("dry run: %v", err)
			}
		})
	}
}

func TestGCKeepsTheFilesInsideASnapshot(t *testing.T) {
	e := newGCEnv(t)
	blob := e.orphan(t, "file content only the snapshot names")
	manifest := `{"version":1,"complete":true,"errors":[],"entries":[{"path":"a.txt","kind":"file","executable":false,"content_hash":"` + blob + `","size":10}]}` + "\n"
	snap := e.orphan(t, manifest)
	e.sd.exec(t, `INSERT INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('run',?,'snapshot',?)`, e.sd.RunID, snap)
	rep, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if has(rep.Deleted, blob) || !e.exists(blob) || !e.exists(snap) {
		t.Fatalf("a file named only inside a referenced snapshot was collected: %+v", rep)
	}
}

func TestGCReportsMissingAndCorruptReferencedBlobsAndNeverCollectsThem(t *testing.T) {
	e := newGCEnv(t)
	gone, bad := e.orphan(t, "will go missing"), e.orphan(t, "will be corrupted")
	for _, h := range []string{gone, bad} {
		e.sd.exec(t, `INSERT INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES('run',?,'x',?)`, e.sd.RunID, h)
	}
	if err := os.Remove(e.file(gone)); err != nil {
		t.Fatal(err)
	}
	os.Chmod(e.file(bad), 0o644)
	if err := os.WriteFile(e.file(bad), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !has(rep.Missing, gone) || !has(rep.Corrupt, bad) {
		t.Fatalf("missing %v corrupt %v", rep.Missing, rep.Corrupt)
	}
	if has(rep.Candidates, gone) || has(rep.Candidates, bad) || has(rep.Deleted, bad) {
		t.Error("a referenced blob was treated as garbage")
	}
	if !e.exists(bad) {
		t.Error("the corrupt blob was deleted: it is evidence, not garbage")
	}
	var n int
	e.s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE hash=?`, gone).Scan(&n)
	if n != 1 {
		t.Error("the metadata of a missing referenced blob was deleted")
	}
}

func TestGCIgnoresWhatIsNotAnArtifactFile(t *testing.T) {
	e := newGCEnv(t)
	dir := filepath.Join(e.root, "artifacts", "sha256", "ab")
	os.MkdirAll(dir, 0o700)
	stray := filepath.Join(dir, "not-a-hash.txt")
	os.WriteFile(stray, []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(e.root, "artifacts", "sha256", "aa"), 0o700)
	link := filepath.Join(e.root, "artifacts", "sha256", "aa", strings.Repeat("a", 64))
	if err := os.Symlink(stray, link); err != nil {
		t.Fatal(err)
	}
	rep, err := e.s.GC(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if has(rep.Deleted, strings.Repeat("a", 64)) {
		t.Errorf("a symlink named like an artifact was treated as one: %v", rep.Deleted)
	}
	for _, p := range []string{stray, link} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}
