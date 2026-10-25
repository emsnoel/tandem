package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/workflow"
)

// importHooks fail ImportLegacy at named points. Only tests set them.
type importHooks struct {
	// afterCopy runs once the archive's bytes are copied and before the source
	// is re-verified, where a concurrent edit would land.
	afterCopy    func()
	beforeCommit func() error
}

// ImportResult describes one import-legacy call.
type ImportResult struct {
	// Imported is false when this archive's content was imported before; RunID
	// is then the run that import created.
	Imported bool   `json:"imported"`
	RunID    string `json:"run_id"`
	Digest   string `json:"digest"`
	Tasks    int    `json:"tasks"`
}

// unverifiable lists what a legacy build never recorded in a form the durable
// engine can check. They are marked unknown, not reconstructed.
var unverifiable = []string{"start_snapshot", "end_snapshot", "patch", "check_certificate", "review_result", "session_identity", "delivery_receipt"}

// ImportLegacy preserves a finished or stopped legacy build, taken from a
// verified archive made by `tandem build archive`, as an archived run with its
// tasks and every evidence file stored as an artifact. It does not create
// attempts, jobs, certificates or review results: history is kept as history,
// and nothing imported can later count as accepted work.
//
// It copies and verifies before it writes any row, re-verifies the source
// after copying so an archive that changed mid-read yields no receipt, and
// records the run, tasks, references and the import receipt in one
// transaction. Importing the same content again, under any path, returns the
// first run with Imported=false.
func (s *Store) ImportLegacy(ctx context.Context, id Identity, archiveDir string) (ImportResult, error) {
	if s.readOnly {
		return ImportResult{}, fmt.Errorf("%w: the store is open read-only", workflow.ErrUnsupported)
	}
	archiveDir, err := filepath.Abs(archiveDir)
	if err != nil {
		return ImportResult{}, err
	}
	src, err := readArchive(archiveDir)
	if err != nil {
		return ImportResult{}, err
	}
	if src.build.State != build.StateFinished && src.build.State != build.StateStopped {
		return ImportResult{}, fmt.Errorf("%w: the legacy build %s is %q; only a finished or stopped build can be imported",
			workflow.ErrBusy, src.build.ID, src.build.State)
	}
	if existing, ok, err := s.importedRun(ctx, src.digest); err != nil {
		return ImportResult{}, err
	} else if ok {
		return ImportResult{Imported: false, RunID: existing, Digest: src.digest, Tasks: len(src.build.Tasks)}, nil
	}

	root := filepath.Dir(s.path)
	store, err := artifact.Open(root)
	if err != nil {
		return ImportResult{}, err
	}
	locker, err := OpenLocker(root)
	if err != nil {
		return ImportResult{}, err
	}
	// Held from the first copied byte until the references commit, so
	// maintenance cannot collect blobs that no row names yet.
	unlock, err := locker.LockShared(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	defer unlock()

	hashes := map[string]string{} // archive-relative path -> artifact hash
	sizes := map[string]int64{}
	for _, f := range src.manifest.Files {
		fh, err := os.Open(filepath.Join(archiveDir, filepath.FromSlash(f.Path)))
		if err != nil {
			return ImportResult{}, err
		}
		got, err := store.Put(ctx, fh)
		fh.Close()
		if err != nil {
			return ImportResult{}, err
		}
		if got != f.SHA256 {
			return ImportResult{}, fmt.Errorf("%w: %s changed while it was being read", workflow.ErrConflict, f.Path)
		}
		hashes[f.Path], sizes[got] = got, f.Size
	}
	manifestHash, err := putFile(ctx, store, filepath.Join(archiveDir, "manifest.json"), sizes)
	if err != nil {
		return ImportResult{}, err
	}
	prov, taskHashes := src.provenance(hashes)
	provBytes, err := json.MarshalIndent(prov, "", "  ")
	if err != nil {
		return ImportResult{}, err
	}
	put := func(content []byte) (string, error) {
		h, err := store.Put(ctx, strings.NewReader(string(content)))
		sizes[h] = int64(len(content))
		return h, err
	}
	provHash, err := put(append(provBytes, '\n'))
	if err != nil {
		return ImportResult{}, err
	}
	unknownHash, err := put([]byte("not recorded by the legacy engine\n"))
	if err != nil {
		return ImportResult{}, err
	}
	planBytes, _ := json.Marshal(map[string]any{"available": false, "plan_path": src.build.Plan, "plan_sha256": src.build.PlanSHA})
	planHash, err := put(append(planBytes, '\n'))
	if err != nil {
		return ImportResult{}, err
	}
	cfgBytes, _ := json.Marshal(map[string]any{
		"builder": src.build.Builder, "planner": src.build.Planner, "check_command": src.build.Check,
		"task_input_budget": src.build.Budget, "judge": src.build.Judge,
	})
	configHash, err := put(append(cfgBytes, '\n'))
	if err != nil {
		return ImportResult{}, err
	}

	if s.importHooks.afterCopy != nil {
		s.importHooks.afterCopy()
	}
	// The source must still be exactly what was verified and copied.
	again, err := readArchive(archiveDir)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: the archive changed during the import (%v); no receipt was written", workflow.ErrConflict, err)
	}
	if again.digest != src.digest {
		return ImportResult{}, fmt.Errorf("%w: the archive changed during the import; no receipt was written", workflow.ErrConflict)
	}

	return s.recordImport(ctx, id, src, importBlobs{
		manifest: manifestHash, provenance: provHash, unknown: unknownHash, plan: planHash, config: configHash,
		tasks: taskHashes, sizes: sizes, files: hashes,
	})
}

type importBlobs struct {
	manifest, provenance, unknown, plan, config string
	tasks                                       []taskBlobs
	sizes                                       map[string]int64
	files                                       map[string]string
}

type taskBlobs struct{ brief string }

func putFile(ctx context.Context, store *artifact.Store, path string, sizes map[string]int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	h, err := store.Put(ctx, f)
	sizes[h] = info.Size()
	return h, err
}

func (s *Store) importedRun(ctx context.Context, digest string) (string, bool, error) {
	var run string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM imports WHERE source_digest=?`, digest).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return run, err == nil, err
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
