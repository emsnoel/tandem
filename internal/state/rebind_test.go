package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// rebindEnv binds a linked worktree of a repository to a checkout, with one
// finished run recorded under it.
type rebindEnv struct {
	repo, wt, home string
	id             Identity
	s              *Store
}

func newRebindEnv(t *testing.T) *rebindEnv {
	t.Helper()
	repo := repoFixture(t)
	wt := filepath.Join(filepath.Dir(repo), "worktree-a")
	git(t, repo, "worktree", "add", "-q", wt, "-b", "a")
	h := home(t)
	id, err := ResolveIdentity(wt, h)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, id.StateRoot)
	spec := runSpec(t, s, 700)
	spec.CheckoutID, spec.CanonicalRoot, spec.GitCommonDir = id.CheckoutID, id.CanonicalRoot, id.GitCommonDir
	if err := s.CreateRun(ctx, spec); err != nil {
		t.Fatal(err)
	}
	e := &rebindEnv{repo: repo, wt: wt, home: h, id: id, s: s}
	e.setRun("finished")
	return e
}

func (e *rebindEnv) setRun(state string) {
	if _, err := e.s.db.ExecContext(ctx, `UPDATE runs SET state=?`, state); err != nil {
		panic(err)
	}
}

func (e *rebindEnv) registry(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(resolvedHome(t, e.home), registryName))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (e *rebindEnv) dbRoot(t *testing.T) string {
	t.Helper()
	var root string
	if err := e.s.db.QueryRowContext(ctx, `SELECT canonical_root FROM checkouts WHERE id=?`, e.id.CheckoutID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRebindMovesAWorktree(t *testing.T) {
	e := newRebindEnv(t)
	moved := filepath.Join(filepath.Dir(e.repo), "worktree-moved")
	git(t, e.repo, "worktree", "move", e.wt, moved)
	// Before the rebind the moved worktree is unknown, not silently the old one.
	if _, bound, err := LookupIdentity(moved, e.home); err != nil || bound {
		t.Fatalf("bound=%v err=%v", bound, err)
	}
	got, err := Rebind(ctx, e.home, e.id.CheckoutID, moved, RebindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckoutID != e.id.CheckoutID || got.StateRoot != e.id.StateRoot {
		t.Errorf("the checkout changed identity: %+v vs %+v", got, e.id)
	}
	again, bound, err := LookupIdentity(moved, e.home)
	if err != nil || !bound || again.CheckoutID != e.id.CheckoutID {
		t.Fatalf("the new path is not bound to the checkout: %+v %v %v", again, bound, err)
	}
	if e.dbRoot(t) != again.CanonicalRoot {
		t.Errorf("the state binding is %s, the registry %s", e.dbRoot(t), again.CanonicalRoot)
	}
	// Running it again changes nothing.
	if _, err := Rebind(ctx, e.home, e.id.CheckoutID, moved, RebindOptions{}); err != nil {
		t.Errorf("a repeat: %v", err)
	}
}

func TestRebindRejectsWrongRepository(t *testing.T) {
	e := newRebindEnv(t)
	other := repoFixture(t)
	before := e.registry(t)
	if _, err := Rebind(ctx, e.home, e.id.CheckoutID, other, RebindOptions{}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a different repository: %v", err)
	}
	if string(e.registry(t)) != string(before) {
		t.Error("the registry changed")
	}
	if e.dbRoot(t) != e.id.CanonicalRoot {
		t.Error("the state binding changed")
	}
	// A repository that merely has the same name and shape is still another one.
	if _, err := Rebind(ctx, e.home, e.id.CheckoutID, e.repo, RebindOptions{}); err != nil {
		t.Errorf("the main worktree of the same repository is a valid target: %v", err)
	}
}

func TestRebindRefusesWhileWorkMayBeActive(t *testing.T) {
	for name, tc := range map[string]func(e *rebindEnv, t *testing.T){
		"a run is running":  func(e *rebindEnv, t *testing.T) { e.setRun("running") },
		"a run is stopping": func(e *rebindEnv, t *testing.T) { e.setRun("stopping") },
		"the writer lease is held, even expired": func(e *rebindEnv, t *testing.T) {
			if _, err := e.s.AcquireLease(ctx, e.id.CheckoutID, "someone", time.Second); err != nil {
				t.Fatal(err)
			}
		},
		"a delivery's outcome is unknown": func(e *rebindEnv, t *testing.T) {
			e.setRun("paused")
			e.s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
			if _, err := e.s.db.ExecContext(ctx, `INSERT INTO effects(id,run_id,source_event_id,kind,payload_hash,nonce,state,available_ms,created_ms)
				SELECT ?,id,?,'builder_dispatch',?,?,'delivery_unknown',1,1 FROM runs`, hexID(903), hexID(904), hash64("p"), hexID(905)); err != nil {
				t.Fatal(err)
			}
			e.s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		},
		"a job's outcome is unknown": func(e *rebindEnv, t *testing.T) {
			e.setRun("paused")
			e.s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
			if _, err := e.s.db.ExecContext(ctx, `INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,version,is_current,created_ms)
				SELECT ?,id,'task-1',1,'running','pending','pending','available',0,1,1 FROM runs`, hexID(901)); err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.db.ExecContext(ctx, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'builder','unknown',?,?,'herdr')`,
				hexID(902), hexID(901), hash64("i"), hash64("c")); err != nil {
				t.Fatal(err)
			}
			e.s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRebindEnv(t)
			moved := filepath.Join(filepath.Dir(e.repo), "worktree-moved")
			git(t, e.repo, "worktree", "move", e.wt, moved)
			tc(e, t)
			before := e.registry(t)
			if _, err := Rebind(ctx, e.home, e.id.CheckoutID, moved, RebindOptions{}); !errors.Is(err, workflow.ErrBusy) {
				t.Fatalf("err = %v", err)
			}
			if string(e.registry(t)) != string(before) || e.dbRoot(t) != e.id.CanonicalRoot {
				t.Error("a refused rebind changed something")
			}
		})
	}
}

func TestRebindRefusesAPathAnotherCheckoutOwns(t *testing.T) {
	e := newRebindEnv(t)
	sibling := filepath.Join(filepath.Dir(e.repo), "worktree-b")
	git(t, e.repo, "worktree", "add", "-q", sibling, "-b", "b")
	other, err := ResolveIdentity(sibling, e.home)
	if err != nil {
		t.Fatal(err)
	}
	before := e.registry(t)
	if _, err := Rebind(ctx, e.home, e.id.CheckoutID, sibling, RebindOptions{}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a path bound to checkout %s: %v", other.CheckoutID, err)
	}
	if string(e.registry(t)) != string(before) {
		t.Error("the registry changed")
	}
}

func TestRebindOfAMovedRepositoryNeedsSayingSo(t *testing.T) {
	e := newRebindEnv(t)
	// A plain clone moved wholesale: its common directory moved with it.
	solo := repoFixture(t)
	id, err := ResolveIdentity(solo, e.home)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, id.StateRoot)
	spec := runSpec(t, s, 800)
	spec.CheckoutID, spec.CanonicalRoot, spec.GitCommonDir = id.CheckoutID, id.CanonicalRoot, id.GitCommonDir
	if err := s.CreateRun(ctx, spec); err != nil {
		t.Fatal(err)
	}
	s.db.ExecContext(ctx, `UPDATE runs SET state='finished'`)
	moved := filepath.Join(filepath.Dir(solo), "solo-moved")
	if err := os.Rename(solo, moved); err != nil {
		t.Fatal(err)
	}
	before := e.registry(t)
	if _, err := Rebind(ctx, e.home, id.CheckoutID, moved, RebindOptions{}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("a changed git directory with no say-so: %v", err)
	}
	if string(e.registry(t)) != string(before) {
		t.Error("the registry changed")
	}
	if _, err := Rebind(ctx, e.home, id.CheckoutID, moved, RebindOptions{RepositoryMoved: true}); err != nil {
		t.Fatal(err)
	}
	if got, bound, err := LookupIdentity(moved, e.home); err != nil || !bound || got.CheckoutID != id.CheckoutID {
		t.Fatalf("%+v %v %v", got, bound, err)
	}
	// It only holds while the old git directory is gone: another repository
	// cannot be claimed this way while the original still exists.
	third := repoFixture(t)
	if _, err := Rebind(ctx, e.home, e.id.CheckoutID, third, RebindOptions{RepositoryMoved: true}); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("claiming another repository while the original directory still exists: %v", err)
	}
}
