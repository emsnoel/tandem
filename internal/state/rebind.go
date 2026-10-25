package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// RebindOptions loosen what a rebind will accept.
type RebindOptions struct {
	// RepositoryMoved says the repository itself moved, so its git directory
	// is at a new path. It is accepted only when the old git directory is gone:
	// it never lets a checkout be pointed at a second repository.
	RepositoryMoved bool
}

// Rebind points a checkout's state at a worktree that moved. The new path must
// be in the same repository (the same git directory, which is how a linked
// worktree is told from a stranger), must not belong to another checkout, and
// the checkout must be idle: no running or stopping run, no held writer lease
// (an expired one included), no job or delivery whose outcome is open.
//
// The state's own binding is updated first and the registry second, so a crash
// between them leaves the old path unresolvable rather than two checkouts
// answering to one; running the same rebind again finishes it.
func Rebind(ctx context.Context, home, checkoutID, newRoot string, opts RebindOptions) (Identity, error) {
	if !idPattern.MatchString(checkoutID) {
		return Identity{}, fmt.Errorf("%w: %q is not a checkout id", workflow.ErrInvalidTransition, checkoutID)
	}
	base, err := stateHome(home, os.UserConfigDir)
	if err != nil {
		return Identity{}, err
	}
	root, common, err := gitIdentity(newRoot)
	if err != nil {
		return Identity{}, err
	}
	realHome, err := checkHome(base, root)
	if err != nil {
		return Identity{}, err
	}
	scope, err := manifestScope(newRoot, root)
	if err != nil {
		return Identity{}, err
	}

	lock := &Locker{path: filepath.Join(realHome, "registry.lock")}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unlock, err := lock.LockExclusive(lctx)
	if err != nil {
		return Identity{}, fmt.Errorf("locking the state registry: %w", err)
	}
	defer unlock()

	path := filepath.Join(realHome, registryName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Identity{}, fmt.Errorf("%w: there is no state registry under %s", workflow.ErrConflict, realHome)
	}
	if err != nil {
		return Identity{}, err
	}
	var reg registryFile
	if err := json.Unmarshal(raw, &reg); err != nil {
		return Identity{}, fmt.Errorf("the state registry %s is corrupt (left untouched): %w", path, err)
	}
	at := -1
	for i, e := range reg.Checkouts {
		switch {
		case e.ID == checkoutID:
			at = i
		case e.CanonicalRoot == root:
			return Identity{}, fmt.Errorf("%w: %s already belongs to checkout %s", workflow.ErrConflict, root, e.ID)
		}
	}
	if at < 0 {
		return Identity{}, fmt.Errorf("%w: the registry has no checkout %s", workflow.ErrConflict, checkoutID)
	}
	entry := reg.Checkouts[at]
	if entry.GitCommonDir != common {
		if _, statErr := os.Stat(entry.GitCommonDir); statErr == nil {
			return Identity{}, fmt.Errorf("%w: %s is in git directory %s, but checkout %s belongs to %s, which still exists: another repository",
				workflow.ErrConflict, root, common, checkoutID, entry.GitCommonDir)
		}
		if !opts.RepositoryMoved {
			return Identity{}, fmt.Errorf("%w: checkout %s belonged to git directory %s, which is gone; if the whole repository moved to %s, say so with --repository-moved",
				workflow.ErrConflict, checkoutID, entry.GitCommonDir, common)
		}
	}

	st, err := Open(ctx, filepath.Join(realHome, checkoutID))
	if err != nil {
		return Identity{}, err
	}
	defer st.Close()
	if err := st.rebindRow(ctx, checkoutID, root, common); err != nil {
		return Identity{}, err
	}
	reg.Checkouts[at].CanonicalRoot, reg.Checkouts[at].GitCommonDir = root, common
	if err := writeRegistry(realHome, path, reg); err != nil {
		return Identity{}, err
	}
	return Identity{CheckoutID: checkoutID, CanonicalRoot: root, GitCommonDir: common, ManifestRoot: scope,
		StateRoot: filepath.Join(realHome, checkoutID)}, nil
}

// rebindRow checks the checkout is idle and rewrites its binding in one
// transaction, so no job can start between the check and the write.
func (s *Store) rebindRow(ctx context.Context, checkoutID, root, common string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	busy := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{workflow.ErrBusy}, a...)...)
	}
	count := func(query string, args ...any) (n int, err error) {
		err = tx.conn.QueryRowContext(ctx, query, args...).Scan(&n)
		return
	}
	if n, err := count(`SELECT count(*) FROM runs r JOIN projects p ON p.id=r.project_id WHERE p.checkout_id=? AND r.state IN ('running','stopping')`, checkoutID); err != nil {
		return err
	} else if n > 0 {
		return busy("checkout %s has %d run(s) running or stopping; stop and settle them first", checkoutID, n)
	}
	if n, err := count(`SELECT count(*) FROM checkout_leases WHERE checkout_id=? AND state IN ('owned','reconciling')`, checkoutID); err != nil {
		return err
	} else if n > 0 {
		return busy("checkout %s's writer lease is held; an expired lease is not proof that its writer stopped", checkoutID)
	}
	if n, err := count(`SELECT count(*) FROM jobs j JOIN attempts a ON a.id=j.attempt_id JOIN runs r ON r.id=a.run_id JOIN projects p ON p.id=r.project_id
		WHERE p.checkout_id=? AND j.state IN ('pending','running','cancelling','unknown')`, checkoutID); err != nil {
		return err
	} else if n > 0 {
		return busy("checkout %s has %d job(s) whose outcome is open", checkoutID, n)
	}
	if n, err := count(`SELECT count(*) FROM effects e JOIN runs r ON r.id=e.run_id JOIN projects p ON p.id=r.project_id
		WHERE p.checkout_id=? AND e.state IN ('delivering','delivery_unknown')`, checkoutID); err != nil {
		return err
	} else if n > 0 {
		return busy("checkout %s has %d delivery whose outcome is open", checkoutID, n)
	}
	if _, err := tx.conn.ExecContext(ctx, `UPDATE checkouts SET canonical_root=?, git_common_dir=? WHERE id=?`, root, common, checkoutID); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: %s is already recorded for another checkout", workflow.ErrConflict, root)
		}
		return err
	}
	return tx.commit(ctx)
}
