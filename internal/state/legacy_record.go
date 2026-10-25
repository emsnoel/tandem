package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// recordImport writes every row of the import, or none.
func (s *Store) recordImport(ctx context.Context, id Identity, src *verifiedArchive, blobs importBlobs) (res ImportResult, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, mapBusy(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return res, mapBusy(err)
	}
	committed := false
	defer func() {
		if !committed {
			if _, rbErr := conn.ExecContext(context.Background(), `ROLLBACK`); rbErr != nil {
				err = errors.Join(err, rbErr)
			}
		}
	}()

	// A concurrent import of the same content may have finished first.
	var existing string
	switch e := conn.QueryRowContext(ctx, `SELECT run_id FROM imports WHERE source_digest=?`, src.digest).Scan(&existing); {
	case e == nil:
		return ImportResult{Imported: false, RunID: existing, Digest: src.digest, Tasks: len(src.build.Tasks)}, nil
	case !errors.Is(e, sql.ErrNoRows):
		return res, e
	}

	now := s.now().UnixMilli()
	for hash, size := range blobs.sizes {
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,?,?)`,
			hash, size, "application/octet-stream", now); err != nil {
			return res, err
		}
	}

	var owner string
	switch e := conn.QueryRowContext(ctx, `SELECT id FROM checkouts WHERE canonical_root=?`, id.CanonicalRoot).Scan(&owner); {
	case errors.Is(e, sql.ErrNoRows):
		if _, err := conn.ExecContext(ctx, `INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?,?,?,?)`,
			id.CheckoutID, id.CanonicalRoot, id.GitCommonDir, now); err != nil {
			return res, err
		}
	case e != nil:
		return res, e
	case owner != id.CheckoutID:
		return res, fmt.Errorf("%w: %s is recorded as checkout %s, not %s", workflow.ErrConflict, id.CanonicalRoot, owner, id.CheckoutID)
	}
	var projectID string
	switch e := conn.QueryRowContext(ctx, `SELECT id FROM projects WHERE checkout_id=? AND manifest_root=?`, id.CheckoutID, id.ManifestRoot).Scan(&projectID); {
	case errors.Is(e, sql.ErrNoRows):
		if projectID, err = randomID(); err != nil {
			return res, err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?,?)`, projectID, id.CheckoutID, id.ManifestRoot); err != nil {
			return res, err
		}
	case e != nil:
		return res, e
	}

	runID, err := randomID()
	if err != nil {
		return res, err
	}
	created := now
	if t, perr := time.Parse(time.RFC3339, src.build.Created); perr == nil {
		created = t.UnixMilli()
	}
	// engine_version is the schema's only value; state 'archived' and the
	// provenance artifact are what mark this run as imported history.
	if _, err := conn.ExecContext(ctx, `INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,created_ms,archived_ms)
		VALUES(?,?,?,?,2,'archived',?,?)`, runID, projectID, blobs.plan, blobs.config, created, now); err != nil {
		return res, err
	}
	refs := [][3]string{{"run", runID, "plan"}, {"run", runID, "config"}, {"run", runID, "provenance"}, {"run", runID, "manifest"}}
	hashes := []string{blobs.plan, blobs.config, blobs.provenance, blobs.manifest}
	for i, t := range blobs.tasks {
		taskID := fmt.Sprintf("task-%d", i+1)
		if _, err := conn.ExecContext(ctx, `INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?,?,?,?,?,?)`,
			runID, taskID, i+1, t.brief, blobs.unknown, t.brief); err != nil {
			return res, err
		}
		refs = append(refs, [3]string{"task", runID + "/" + taskID, "brief"}, [3]string{"task", runID + "/" + taskID, "constraints"})
		hashes = append(hashes, t.brief, blobs.unknown)
	}
	// Every archived file is referenced by the run, so none is collectable.
	paths := make([]string, 0, len(blobs.files))
	for p := range blobs.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		refs = append(refs, [3]string{"run", runID, "legacy:" + p})
		hashes = append(hashes, blobs.files[p])
	}
	for i, r := range refs {
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_refs(owner_kind,owner_id,role,hash) VALUES(?,?,?,?)`, r[0], r[1], r[2], hashes[i]); err != nil {
			return res, err
		}
	}

	importID, err := randomID()
	if err != nil {
		return res, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO imports(id,source_digest,run_id,manifest_hash,completed_ms) VALUES(?,?,?,?,?)`,
		importID, src.digest, runID, blobs.manifest, now); err != nil {
		return res, err
	}
	if s.importHooks.beforeCommit != nil {
		if err := s.importHooks.beforeCommit(); err != nil {
			return res, err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return res, mapBusy(err)
	}
	committed = true
	return ImportResult{Imported: true, RunID: runID, Digest: src.digest, Tasks: len(src.build.Tasks)}, nil
}
