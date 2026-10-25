package state

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Splits and merges. Both derive new work ids from active work, keep every
// origin budget group of their sources, supersede the sources and record the
// ancestry, so a later alias of a source can be followed or, when it became
// several pieces, refused for explicit reconciliation.

// SplitWork divides active work into pieces that keep every budget group of
// the parent, so a split never mints a repair allowance. Retrying the same
// split succeeds.
func (s *Store) SplitWork(ctx context.Context, req SplitRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	ids := make([]string, 0, len(req.Children))
	for _, c := range req.Children {
		if !idPattern.MatchString(c.ID) || c.Alias == "" {
			return invalid("each piece of a split needs a 32 hex character id and an alias")
		}
		ids = append(ids, c.ID)
	}
	if err := distinctTwoOrMore(ids, "pieces of a split"); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if err := requireOpen(ctx, tx, req.CollaborationID); err != nil {
		return err
	}
	parent, err := workTx(ctx, tx.conn, req.CollaborationID, req.ParentID)
	if err != nil {
		return err
	}
	if done, err := alreadyDerived(ctx, tx, []string{req.ParentID}, ids, "split"); done || err != nil {
		return err
	}
	if parent.State != "active" {
		return fmt.Errorf("%w: work %s was already %s", ErrWorkMapping, parent.ID, parent.State)
	}
	now := s.now().UnixMilli()
	for _, c := range req.Children {
		if err := deriveTx(ctx, tx, req.CollaborationID, c.ID, c.Alias, []string{req.ParentID}, parent.BudgetGroups, "split", now); err != nil {
			return err
		}
	}
	if err := supersedeTx(ctx, tx, []string{req.ParentID}); err != nil {
		return err
	}
	return tx.commit(ctx)
}

// MergeWork joins active works into one that keeps the union of their budget
// groups. An unknown or already superseded source is ErrWorkMapping. Retrying
// the same merge succeeds.
func (s *Store) MergeWork(ctx context.Context, req MergeRequest) error {
	if err := s.writable(); err != nil {
		return err
	}
	if !idPattern.MatchString(req.ID) || req.Alias == "" {
		return invalid("a merge needs a 32 hex character id and an alias")
	}
	if err := distinctTwoOrMore(req.SourceIDs, "merge sources"); err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.rollback()
	if err := requireOpen(ctx, tx, req.CollaborationID); err != nil {
		return err
	}
	if done, err := alreadyDerived(ctx, tx, req.SourceIDs, []string{req.ID}, "merge"); done || err != nil {
		return err
	}
	union := map[string]bool{}
	for _, id := range req.SourceIDs {
		w, err := workTx(ctx, tx.conn, req.CollaborationID, id)
		if err != nil {
			return err
		}
		if w.State != "active" {
			return fmt.Errorf("%w: merge source %s was already %s", ErrWorkMapping, id, w.State)
		}
		for _, g := range w.BudgetGroups {
			union[g] = true
		}
	}
	groups := make([]string, 0, len(union))
	for g := range union {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	if err := deriveTx(ctx, tx, req.CollaborationID, req.ID, req.Alias, req.SourceIDs, groups, "merge", s.now().UnixMilli()); err != nil {
		return err
	}
	if err := supersedeTx(ctx, tx, req.SourceIDs); err != nil {
		return err
	}
	return tx.commit(ctx)
}

func distinctTwoOrMore(ids []string, what string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return invalid("%s repeat %s", what, id)
		}
		seen[id] = true
	}
	if len(ids) < 2 {
		return invalid("%s need at least two; one is a rename (AttachWorkAlias)", what)
	}
	return nil
}

// alreadyDerived reports whether every child exists with exactly these
// parents by this relation, which makes a retry a no-op. A child id that
// exists any other way is ErrConflict.
func alreadyDerived(ctx context.Context, tx *dbTx, parents, children []string, relation string) (bool, error) {
	want := append([]string(nil), parents...)
	sort.Strings(want)
	found := 0
	for _, child := range children {
		rows, err := tx.conn.QueryContext(ctx, `SELECT parent_work_id FROM work_ancestry WHERE child_work_id=? AND relation=? ORDER BY parent_work_id`,
			child, relation)
		if err != nil {
			return false, err
		}
		var got []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return false, err
			}
			got = append(got, p)
		}
		rows.Close()
		var exists int
		if err := tx.conn.QueryRowContext(ctx, `SELECT count(*) FROM logical_work WHERE id=?`, child).Scan(&exists); err != nil {
			return false, err
		}
		switch {
		case exists == 0:
		case strings.Join(got, ",") == strings.Join(want, ","):
			found++
		default:
			return false, fmt.Errorf("%w: work %s exists with other ancestry", workflow.ErrConflict, child)
		}
	}
	if found > 0 && found < len(children) {
		return false, fmt.Errorf("%w: a %s was partly recorded", workflow.ErrConflict, relation)
	}
	return found == len(children), nil
}

func deriveTx(ctx context.Context, tx *dbTx, collab, id, alias string, parents, groups []string, relation string, now int64) error {
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO logical_work(id,collaboration_id,origin_name,state,created_ms) VALUES(?,?,?,'active',?)`,
		id, collab, alias, now); err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO work_budget_groups(logical_work_id,budget_group_id) VALUES(?,?)`, id, g); err != nil {
			return err
		}
	}
	for _, p := range parents {
		if _, err := tx.conn.ExecContext(ctx, `INSERT INTO work_ancestry(parent_work_id,child_work_id,relation,created_ms) VALUES(?,?,?,?)`,
			p, id, relation, now); err != nil {
			return err
		}
	}
	return attachTx(ctx, tx, collab, id, alias, "", "", now)
}

func supersedeTx(ctx context.Context, tx *dbTx, ids []string) error {
	for _, id := range ids {
		if _, err := tx.conn.ExecContext(ctx, `UPDATE logical_work SET state='superseded' WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}
